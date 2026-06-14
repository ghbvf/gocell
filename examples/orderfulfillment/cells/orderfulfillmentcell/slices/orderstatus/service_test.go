package orderstatus_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

// testBundle holds a Service together with its backing repository and
// in-memory read model so tests can inject projection state without running
// a coordinator or Tailer.
type testBundle struct {
	svc  *orderstatus.Service
	repo *mem.OrderRepository
	rm   *projection.MemReadModel
}

func newBundle(t *testing.T) testBundle {
	t.Helper()
	clk := clock.Real()
	repo := mem.NewOrderRepository()
	rm := projection.NewMemReadModel()
	svc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(repo),
		orderstatus.WithOrderStatusReadModel(rm),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return testBundle{svc: svc, repo: repo, rm: rm}
}

// createOrder inserts an order into the repository.
func createOrder(t *testing.T, b testBundle, orderID string) {
	t.Helper()
	ctx := context.Background()
	order := &domain.Order{
		ID:          orderID,
		Item:        "widget",
		AmountCents: 1000,
		CreatedAt:   time.Now(),
	}
	if err := b.repo.Create(ctx, order); err != nil {
		t.Fatalf("repo.Create: %v", err)
	}
}

// makeProjectionEvent creates a synthetic saga-journal projection event for testing.
// The EventID is formatted as "saga-journal:1@<orderID>" which matches the
// sagaProjectionEvent.EventID() format.
func makeProjectionEvent(orderID string, kind string) fakeEvent {
	env := sagaprojection.SagaEventEnvelope{Kind: kind}
	raw, _ := json.Marshal(env)
	return fakeEvent{
		eventID: "saga-journal:1@" + orderID,
		payload: raw,
	}
}

// fakeEvent is a test-only implementation of cellvocab.ProjectionEvent.
type fakeEvent struct {
	eventID string
	payload []byte
}

func (f fakeEvent) EventID() string                                    { return f.eventID }
func (f fakeEvent) Payload() []byte                                    { return f.payload }
func (f fakeEvent) OccurredAt() time.Time                              { return time.Now() }
func (f fakeEvent) Stream() string                                     { return sagaprojection.SagaJournalStream }
func (f fakeEvent) RestoreContext(ctx context.Context) context.Context { return ctx }

// TestGetOrderStatus_NotFound verifies 404 when the order does not exist.
func TestGetOrderStatus_NotFound(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	_, err := b.svc.GetOrderStatus(context.Background(), "ord-nonexistent")
	errcodetest.AssertCode(t, err, errcode.ErrOrderNotFound)
}

// TestGetOrderStatus_Accepted verifies "accepted" when order exists but the
// projection has no row yet (the saga Tailer hasn't applied any event).
func TestGetOrderStatus_Accepted(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	orderID := "ord-accepted-1"
	createOrder(t, b, orderID)
	// No read-model row → accepted.
	status, err := b.svc.GetOrderStatus(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrderStatus: %v", err)
	}
	if status != orderstatusgen.ResponseDataStatusAccepted {
		t.Errorf("status = %q, want accepted", status)
	}
}

// TestGetOrderStatus_StatusTable covers all stored statuses by pre-seeding the
// read model directly (simulating what the Tailer would apply).
func TestGetOrderStatus_StatusTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seedStatus orderstatusgen.ResponseDataStatus
		wantStatus orderstatusgen.ResponseDataStatus
	}{
		{
			name:       "running",
			seedStatus: orderstatusgen.ResponseDataStatusRunning,
			wantStatus: orderstatusgen.ResponseDataStatusRunning,
		},
		{
			name:       "succeeded",
			seedStatus: orderstatusgen.ResponseDataStatusSucceeded,
			wantStatus: orderstatusgen.ResponseDataStatusSucceeded,
		},
		{
			name:       "compensated",
			seedStatus: orderstatusgen.ResponseDataStatusCompensated,
			wantStatus: orderstatusgen.ResponseDataStatusCompensated,
		},
		{
			name:       "failed",
			seedStatus: orderstatusgen.ResponseDataStatusFailed,
			wantStatus: orderstatusgen.ResponseDataStatusFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBundle(t)
			orderID := "ord-" + tc.name
			createOrder(t, b, orderID)
			// Pre-seed the read model (simulating a Tailer applying events).
			if err := b.rm.Upsert(context.Background(), orderID, tc.seedStatus); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			status, err := b.svc.GetOrderStatus(context.Background(), orderID)
			if err != nil {
				t.Fatalf("GetOrderStatus: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

// TestNewService_MissingDeps verifies fail-fast on missing required dependencies.
func TestNewService_MissingDeps(t *testing.T) {
	t.Parallel()
	clk := clock.Real()
	rm := projection.NewMemReadModel()

	t.Run("missing_repo", func(t *testing.T) {
		t.Parallel()
		_, err := orderstatus.NewService(clk, orderstatus.WithOrderStatusReadModel(rm))
		if err == nil {
			t.Fatal("expected error for missing repo, got nil")
		}
	})

	t.Run("missing_read_model", func(t *testing.T) {
		t.Parallel()
		_, err := orderstatus.NewService(clk, orderstatus.WithOrderRepository(mem.NewOrderRepository()))
		if err == nil {
			t.Fatal("expected error for missing read model, got nil")
		}
	})
}

// TestHandleOrderEvent_SeedReadModel verifies that HandleOrderEvent applies a saga
// event to the read model.
func TestHandleOrderEvent_SeedReadModel(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	orderID := "ord-handle-1"
	ctx := context.Background()

	// Feed a step-started event → should write "running" to the read model.
	ev := makeProjectionEvent(orderID, "step_started")
	if err := b.svc.HandleOrderEvent(ctx, ev); err != nil {
		t.Fatalf("HandleOrderEvent: %v", err)
	}
	status, found, err := b.rm.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("read model has no row after HandleOrderEvent")
	}
	if status != orderstatusgen.ResponseDataStatusRunning {
		t.Errorf("status = %q, want running", status)
	}
}

// TestHandleOrderEvent_Terminal verifies that a terminal kind writes the terminal
// status to the read model.
func TestHandleOrderEvent_Terminal(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	orderID := "ord-terminal-1"
	ctx := context.Background()

	ev := makeProjectionEvent(orderID, "saga_succeeded")
	if err := b.svc.HandleOrderEvent(ctx, ev); err != nil {
		t.Fatalf("HandleOrderEvent: %v", err)
	}
	status, found, err := b.rm.Get(ctx, orderID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("read model has no row after HandleOrderEvent")
	}
	if status != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("status = %q, want succeeded", status)
	}
}

// TestHandleOrderEvent_BadJSON verifies that a bad payload returns a permanent error.
func TestHandleOrderEvent_BadJSON(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	ev := fakeEvent{
		eventID: "saga-journal:1@ord-bad-json",
		payload: []byte("{not valid json"),
	}
	err := b.svc.HandleOrderEvent(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for bad JSON payload, got nil")
	}
	var pe *outbox.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}

// TestHandleOrderEvent_UnknownKind verifies that an unknown kind returns a permanent error.
func TestHandleOrderEvent_UnknownKind(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	ev := makeProjectionEvent("ord-unknown-kind", "not_a_real_kind")
	err := b.svc.HandleOrderEvent(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
	var pe *outbox.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}

// TestHandleOrderEvent_MalformedEventID verifies that a malformed EventID (no @)
// returns a permanent error. A permanent apply error HALTS the Tailer's
// checkpoint advance (fail-closed); it does NOT route to a DLX (the saga-journal
// Tailer has no dead-letter path). The test asserts only the PermanentError
// classification — the halt-not-DLX behavior lives in runtime/saga/tailer.
func TestHandleOrderEvent_MalformedEventID(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	env := sagaprojection.SagaEventEnvelope{Kind: "step_started"}
	raw, _ := json.Marshal(env)
	ev := fakeEvent{
		eventID: "saga-journal:1-no-at-sign",
		payload: raw,
	}
	err := b.svc.HandleOrderEvent(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error for malformed EventID, got nil")
	}
	var pe *outbox.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}
