package webhook_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	rtwh "github.com/ghbvf/gocell/runtime/webhook"
)

func validReceiverRequest(t *testing.T) cell.WebhookReceiverRequest {
	t.Helper()
	return cell.WebhookReceiverRequest{
		Spec:    testSpec(),
		Handler: func(_ context.Context, _ kwh.Delivery) error { return nil },
	}
}

func TestBuildRouteGroups_HappyPath(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	reqs := []cell.WebhookReceiverRequest{
		validReceiverRequest(t),
	}

	groups, err := rtwh.BuildRouteGroups(clk, reqs, store, claimer, kwh.Metrics{})
	if err != nil {
		t.Fatalf("BuildRouteGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}

	g := groups[0]

	// Listener must be PrimaryListener.
	if g.Listener != cell.PrimaryListener {
		t.Errorf("Listener = %v, want PrimaryListener", g.Listener)
	}
	// Prefix must match PathPattern.
	if g.Prefix != testPathPattern {
		t.Errorf("Prefix = %q, want %q", g.Prefix, testPathPattern)
	}
	// CellID must match spec.CellID.
	if g.CellID != testCellID {
		t.Errorf("CellID = %q, want %q", g.CellID, testCellID)
	}
	// Register must not be nil.
	if g.Register == nil {
		t.Error("Register is nil")
	}
}

func TestBuildRouteGroups_MultipleRequests(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	spec2 := testSpec()
	spec2.ContractID = "webhook.test2.v1"
	spec2.PathPattern = "/webhooks/test2"
	spec2.CellID = "testcell2"

	reqs := []cell.WebhookReceiverRequest{
		validReceiverRequest(t),
		{
			Spec:    spec2,
			Handler: func(_ context.Context, _ kwh.Delivery) error { return nil },
		},
	}

	groups, err := rtwh.BuildRouteGroups(clk, reqs, store, claimer, kwh.Metrics{})
	if err != nil {
		t.Fatalf("BuildRouteGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
}

func TestBuildRouteGroups_EmptyReqs(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	groups, err := rtwh.BuildRouteGroups(clk, nil, store, claimer, kwh.Metrics{})
	if err != nil {
		t.Fatalf("BuildRouteGroups(nil): %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("got %d groups, want 0", len(groups))
	}
}

func TestBuildRouteGroups_NilStore(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	claimer := idempotency.NewInMemClaimer(clk)

	_, err := rtwh.BuildRouteGroups(clk, nil, nil, claimer, kwh.Metrics{})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestBuildRouteGroups_NilClaimer(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)

	_, err := rtwh.BuildRouteGroups(clk, nil, store, nil, kwh.Metrics{})
	if err == nil {
		t.Fatal("expected error for nil claimer")
	}
}

func TestBuildRouteGroups_InvalidSpec(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	// Zero-value spec should fail Validate().
	reqs := []cell.WebhookReceiverRequest{
		{
			Spec:    kwh.ReceiverSpec{},
			Handler: func(_ context.Context, _ kwh.Delivery) error { return nil },
		},
	}

	_, err := rtwh.BuildRouteGroups(clk, reqs, store, claimer, kwh.Metrics{})
	if err == nil {
		t.Fatal("expected error for invalid spec")
	}
}

func TestBuildRouteGroups_Register_IsMountCall(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	groups, err := rtwh.BuildRouteGroups(clk, []cell.WebhookReceiverRequest{validReceiverRequest(t)}, store, claimer, kwh.Metrics{})
	if err != nil {
		t.Fatalf("BuildRouteGroups: %v", err)
	}

	// Register mounts at "/" so that the adapter composes
	// joinPrefix(Prefix, "/") == Prefix — no double-prefix.
	// Prefix (RouteGroup.Prefix) carries the full path pattern.
	var mountPattern string
	mux := &recordingMux{onMount: func(p string) { mountPattern = p }}
	if err := groups[0].Register(mux); err != nil {
		t.Fatalf("Register: %v", err)
	}
	const wantMountArg = "/"
	if mountPattern != wantMountArg {
		t.Errorf("mounted pattern = %q, want %q (Prefix carries the full path; Register uses '/' to avoid double-prefix)",
			mountPattern, wantMountArg)
	}
}

// recordingMux is a minimal RouteMux stub for testing Register callbacks.
type recordingMux struct {
	onMount func(string)
}

func (m *recordingMux) Handle(_ string, _ http.Handler) {}
func (m *recordingMux) Route(_ string, fn func(cell.RouteMux)) {
	fn(m)
}

func (m *recordingMux) Mount(pattern string, _ http.Handler) {
	if m.onMount != nil {
		m.onMount(pattern)
	}
}
func (m *recordingMux) Group(fn func(cell.RouteMux)) { fn(m) }
func (m *recordingMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux {
	return m
}

// TestBuildRouteGroups_NewReceiverError covers buildRouteGroup's branch where
// NewHMACVerifier succeeds (ToleranceSeconds > 0) but NewReceiver fails because
// the spec is otherwise invalid (empty ContractID). This exercises the
// post-verifier NewReceiver error return that an all-zero spec does not reach
// (an all-zero spec fails earlier at WithTolerance(0)).
func TestBuildRouteGroups_NewReceiverError(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	spec := testSpec()
	spec.ContractID = "" // tolerance stays valid → verifier OK → NewReceiver.Validate fails

	reqs := []cell.WebhookReceiverRequest{
		{
			Spec:    spec,
			Handler: func(_ context.Context, _ kwh.Delivery) error { return nil },
		},
	}

	_, err := rtwh.BuildRouteGroups(clk, reqs, store, claimer, kwh.Metrics{})
	if err == nil {
		t.Fatal("expected error from NewReceiver inside buildRouteGroup")
	}
}
