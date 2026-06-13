package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Compile-time check: IdempotencyCollector must implement MetricsObserver.
var _ idemhttp.MetricsObserver = (*obmetrics.IdempotencyCollector)(nil)

// TestNewIdempotencyCollector_RejectsNilProvider asserts fail-fast on nil
// provider (KindInvalid error, no fallback).
func TestNewIdempotencyCollector_RejectsNilProvider(t *testing.T) {
	_, err := obmetrics.NewIdempotencyCollector(nil)
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error must be *errcode.Error; got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInvalid {
		t.Errorf("error Kind = %v, want KindInvalid", ec.Kind)
	}
}

// TestNewIdempotencyCollector_RegistersOneCounter asserts exactly one counter
// (idempotency_requests_total) is registered with label names ["cell","state"].
func TestNewIdempotencyCollector_RegistersOneCounter(t *testing.T) {
	p := newSagaSpyProvider()
	_, err := obmetrics.NewIdempotencyCollector(p)
	if err != nil {
		t.Fatalf("NewIdempotencyCollector: %v", err)
	}

	if len(p.counterNames) != 1 {
		t.Errorf("counter count = %d, want 1 (got %v)", len(p.counterNames), p.counterNames)
	}
	if _, ok := p.counterNames["idempotency_requests_total"]; !ok {
		t.Errorf("idempotency_requests_total counter not registered; got %v", p.counterNames)
	}
	got := p.counterLabels["idempotency_requests_total"]
	want := []string{"cell", "state"}
	if !equalStringSlice(got, want) {
		t.Errorf("idempotency_requests_total labels = %v, want %v", got, want)
	}

	if len(p.gaugeNames) != 0 {
		t.Errorf("IdempotencyCollector must not register gauges, got %v", p.gaugeNames)
	}
	if len(p.histogramNames) != 0 {
		t.Errorf("IdempotencyCollector must not register histograms, got %v", p.histogramNames)
	}
}

// TestIdempotencyCollector_ObserveRequest_AllStates verifies all seven
// RequestState constants emit the correct wire-stable state label value.
func TestIdempotencyCollector_ObserveRequest_AllStates(t *testing.T) {
	cases := []struct {
		state idemhttp.RequestState
		want  string
	}{
		{idemhttp.StateAcquired, "acquired"},
		{idemhttp.StateReplayed, "replayed"},
		{idemhttp.StateBusy, "busy"},
		{idemhttp.StateStoreError, "store_error"},
		{idemhttp.StateOversize, "oversize"},
		{idemhttp.StateKeyReused, "key_reused"},
		{idemhttp.StateBodyReadFailed, "body_read_failed"},
	}

	p := newSagaSpyProvider()
	c, err := obmetrics.NewIdempotencyCollector(p)
	if err != nil {
		t.Fatalf("NewIdempotencyCollector: %v", err)
	}

	// A cell-bearing ctx lets each state path also assert the cell label is
	// emitted alongside state (the two labels are resolved independently).
	ctx := ctxkeys.WithCellID(context.Background(), "accesscore")
	for _, tc := range cases {
		c.ObserveRequest(ctx, tc.state)
	}

	ops := p.counterOps["idempotency_requests_total"]
	if len(ops) != len(cases) {
		t.Fatalf("counter ops = %d, want %d", len(ops), len(cases))
	}
	for i, tc := range cases {
		if got := ops[i].labels["state"]; got != tc.want {
			t.Errorf("ops[%d] state = %q, want %q", i, got, tc.want)
		}
		if got := ops[i].labels["cell"]; got != "accesscore" {
			t.Errorf("ops[%d] cell = %q, want accesscore", i, got)
		}
	}
}

// TestIdempotencyCollector_CellLabel_FromCtx verifies that the cell label is
// resolved from ctxkeys.CellID when present.
func TestIdempotencyCollector_CellLabel_FromCtx(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewIdempotencyCollector(p)
	if err != nil {
		t.Fatalf("NewIdempotencyCollector: %v", err)
	}

	ctx := ctxkeys.WithCellID(context.Background(), "accesscore")
	c.ObserveRequest(ctx, idemhttp.StateAcquired)

	ops := p.counterOps["idempotency_requests_total"]
	if len(ops) != 1 {
		t.Fatalf("counter ops = %d, want 1", len(ops))
	}
	if got := ops[0].labels["cell"]; got != "accesscore" {
		t.Errorf("cell = %q, want accesscore", got)
	}
}

// TestIdempotencyCollector_CellLabel_FallsBackToRuntime verifies that the cell
// label falls back to RuntimeCellSentinel ("_runtime") when ctx carries no
// CellID.
func TestIdempotencyCollector_CellLabel_FallsBackToRuntime(t *testing.T) {
	p := newSagaSpyProvider()
	c, err := obmetrics.NewIdempotencyCollector(p)
	if err != nil {
		t.Fatalf("NewIdempotencyCollector: %v", err)
	}

	ctx := context.Background() // no CellID in ctx
	c.ObserveRequest(ctx, idemhttp.StateReplayed)

	ops := p.counterOps["idempotency_requests_total"]
	if len(ops) != 1 {
		t.Fatalf("counter ops = %d, want 1", len(ops))
	}
	if got := ops[0].labels["cell"]; got != obmetrics.RuntimeCellSentinel {
		t.Errorf("cell = %q, want %q (RuntimeCellSentinel)", got, obmetrics.RuntimeCellSentinel)
	}
}

// TestNewIdempotencyCollector_RegistrationFailure_ReturnsError verifies that a
// CounterVec registration failure is propagated as a wrapped error (no partial
// state retained). Unlike SagaCollector there is only one counter so no LIFO
// rollback is needed, but the error must still be returned.
func TestNewIdempotencyCollector_RegistrationFailure_ReturnsError(t *testing.T) {
	p := newSagaSpyProvider()
	p.failOnName = "idempotency_requests_total"
	_, err := obmetrics.NewIdempotencyCollector(p)
	if err == nil {
		t.Fatal("expected an error when CounterVec registration fails")
	}
	if p.unregisterCount != 0 {
		t.Errorf("unregisterCount = %d, want 0 (single counter, no rollback required)", p.unregisterCount)
	}
}

// TestNewIdempotencyCollector_NopProvider verifies that a NopProvider succeeds
// (no error) so callers can use kernelmetrics.NopProvider{} as an explicit
// disable shim.
func TestNewIdempotencyCollector_NopProvider(t *testing.T) {
	_, err := obmetrics.NewIdempotencyCollector(kernelmetrics.NopProvider{})
	if err != nil {
		t.Fatalf("NewIdempotencyCollector(NopProvider): %v", err)
	}
}
