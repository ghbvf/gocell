package orderstatus_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testBundle holds a Service together with its backing repository and
// MemJournal so tests can inject saga events without running a coordinator.
type testBundle struct {
	svc  *orderstatus.Service
	repo *mem.OrderRepository
	jrnl *journal.MemJournal
}

func newBundle(t *testing.T) testBundle {
	t.Helper()
	clk := clock.Real()
	repo := mem.NewOrderRepository()
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}
	svc, err := orderstatus.NewService(
		clk,
		orderstatus.WithOrderRepository(repo),
		orderstatus.WithJournal(jrnl),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return testBundle{svc: svc, repo: repo, jrnl: jrnl}
}

// createOrder inserts an order into the repository and enrolls a saga instance
// in the journal for it. Returns the order ID (= saga instance ID).
func createOrder(t *testing.T, b testBundle) string {
	t.Helper()
	ctx := context.Background()
	orderID := "ord-" + time.Now().Format("20060102150405.999999999")
	order := &domain.Order{
		ID:          orderID,
		Item:        "widget",
		AmountCents: 1000,
		CreatedAt:   time.Now(),
	}
	if err := b.repo.Create(ctx, order); err != nil {
		t.Fatalf("repo.Create: %v", err)
	}

	defID, err := idutil.NewUUID()
	if err != nil {
		t.Fatalf("NewUUID for definitionID: %v", err)
	}
	inst := saga.NewInstance(idutil.SafeID(orderID), idutil.SafeID(defID), time.Now())
	if err := b.jrnl.Enqueue(ctx, inst); err != nil {
		t.Fatalf("jrnl.Enqueue: %v", err)
	}
	return orderID
}

// claimLease claims the instance lease from the journal and returns the leaseID.
func claimLease(t *testing.T, jrnl *journal.MemJournal) idutil.SafeID {
	t.Helper()
	ctx := context.Background()
	claimed, leaseID, err := jrnl.ClaimPending(ctx, 10, testtime.D30s)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) == 0 {
		t.Fatal("ClaimPending: no instances claimed")
	}
	return leaseID
}

// mustAppend appends ev to the instance log under leaseID, failing on error.
func mustAppend(t *testing.T, jrnl *journal.MemJournal, orderID string, leaseID idutil.SafeID, ev journal.Event) {
	t.Helper()
	if _, err := jrnl.Append(context.Background(), idutil.SafeID(orderID), leaseID, ev); err != nil {
		t.Fatalf("Append %s: %v", ev.Kind, err)
	}
}

// mustMarkTerminal marks the instance terminal with status, failing on error or
// ok=false.
func mustMarkTerminal(t *testing.T, jrnl *journal.MemJournal, orderID string, leaseID idutil.SafeID, status saga.Status) {
	t.Helper()
	ok, err := jrnl.MarkTerminal(context.Background(), idutil.SafeID(orderID), leaseID, status)
	if err != nil {
		t.Fatalf("MarkTerminal %s: %v", status, err)
	}
	if !ok {
		t.Fatalf("MarkTerminal %s: returned ok=false", status)
	}
}

func TestGetOrderStatus_NotFound(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	_, err := b.svc.GetOrderStatus(context.Background(), "ord-nonexistent")
	errcodetest.AssertCode(t, err, errcode.ErrOrderNotFound)
}

func TestGetOrderStatus_Accepted(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	// createOrder both persists the order and enrolls a saga instance via
	// Enqueue. Load then returns an empty (non-nil) slice, so deriveStatus
	// returns StatusAccepted — the expected post-enrollment, pre-step state.
	orderID := createOrder(t, b)
	status, err := b.svc.GetOrderStatus(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrderStatus: %v", err)
	}
	if status != orderstatusgen.ResponseDataStatusAccepted {
		t.Errorf("status = %q, want %q", status, orderstatusgen.ResponseDataStatusAccepted)
	}
}

func TestGetOrderStatus_OrphanOrder(t *testing.T) {
	t.Parallel()
	b := newBundle(t)
	// Persist the order without enrolling a saga instance. This simulates an
	// orphan order where Enqueue failed after the order write. After F1, the
	// service must return an error (journal.Load → KindNotFound) instead of
	// silently returning StatusAccepted, which would mask the enrollment bug.
	ctx := context.Background()
	order := &domain.Order{ID: "ord-orphan", Item: "widget", AmountCents: 1000, CreatedAt: time.Now()}
	if err := b.repo.Create(ctx, order); err != nil {
		t.Fatalf("repo.Create: %v", err)
	}
	_, err := b.svc.GetOrderStatus(ctx, "ord-orphan")
	if err == nil {
		t.Fatal("GetOrderStatus: expected error for orphan order (never enrolled), got nil")
	}
}

func TestNewService_MissingDeps(t *testing.T) {
	t.Parallel()
	clk := clock.Real()

	t.Run("missing_repo", func(t *testing.T) {
		t.Parallel()
		jrnl, err := journal.NewMemJournal(clk)
		if err != nil {
			t.Fatalf("NewMemJournal: %v", err)
		}
		_, err = orderstatus.NewService(clk, orderstatus.WithJournal(jrnl))
		if err == nil {
			t.Fatal("expected error for missing repo, got nil")
		}
	})

	t.Run("missing_journal", func(t *testing.T) {
		t.Parallel()
		_, err := orderstatus.NewService(clk, orderstatus.WithOrderRepository(mem.NewOrderRepository()))
		if err == nil {
			t.Fatal("expected error for missing journal, got nil")
		}
	})
}

// TestGetOrderStatus_StatusTable covers all five status outcomes of deriveStatus
// by injecting journal events via the real MemJournal API.
func TestGetOrderStatus_StatusTable(t *testing.T) {
	t.Parallel()

	type setup func(t *testing.T, b testBundle, orderID string)

	tests := []struct {
		name       string
		setup      setup
		wantStatus orderstatusgen.ResponseDataStatus
	}{
		{
			name: "running_after_step_completed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindStepCompleted, StepName: "reserve"})
			},
			wantStatus: orderstatusgen.ResponseDataStatusRunning,
		},
		{
			name: "succeeded",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Running (step event), then mark terminal Succeeded.
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindStepStarted, StepName: "reserve"})
				mustMarkTerminal(t, b.jrnl, orderID, leaseID, saga.StatusSucceeded)
			},
			wantStatus: orderstatusgen.ResponseDataStatusSucceeded,
		},
		{
			name: "compensated",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Running → Compensating → Compensated (terminal).
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindStepStarted, StepName: "reserve"})
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindCompensationStarted})
				mustMarkTerminal(t, b.jrnl, orderID, leaseID, saga.StatusCompensated)
			},
			wantStatus: orderstatusgen.ResponseDataStatusCompensated,
		},
		{
			name: "failed_via_saga_failed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Failed directly (no steps committed, no compensation).
				mustMarkTerminal(t, b.jrnl, orderID, leaseID, saga.StatusFailed)
			},
			wantStatus: orderstatusgen.ResponseDataStatusFailed,
		},
		{
			name: "failed_via_saga_expired",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				mustMarkTerminal(t, b.jrnl, orderID, leaseID, saga.StatusExpired)
			},
			wantStatus: orderstatusgen.ResponseDataStatusFailed,
		},
		{
			name: "failed_via_saga_compensation_failed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Running → Compensating → CompensationFailed.
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindStepStarted, StepName: "reserve"})
				mustAppend(t, b.jrnl, orderID, leaseID, journal.Event{Kind: journal.KindCompensationStarted})
				mustMarkTerminal(t, b.jrnl, orderID, leaseID, saga.StatusCompensationFailed)
			},
			wantStatus: orderstatusgen.ResponseDataStatusFailed,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBundle(t)
			orderID := createOrder(t, b)
			tc.setup(t, b, orderID)

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
