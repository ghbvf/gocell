package orderstatus_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/idutil"
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
	claimed, leaseID, err := jrnl.ClaimPending(ctx, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) == 0 {
		t.Fatal("ClaimPending: no instances claimed")
	}
	return leaseID
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
	if status != orderstatus.StatusAccepted {
		t.Errorf("status = %q, want %q", status, orderstatus.StatusAccepted)
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
		wantStatus string
	}{
		{
			name: "running_after_step_completed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				_, err := b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind:     journal.KindStepCompleted,
					StepName: "reserve",
				})
				if err != nil {
					t.Fatalf("Append KindStepCompleted: %v", err)
				}
			},
			wantStatus: orderstatus.StatusRunning,
		},
		{
			name: "succeeded",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				// Advance to Running first (Pending → Running via step event),
				// then mark terminal Succeeded.
				_, err := b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind:     journal.KindStepStarted,
					StepName: "reserve",
				})
				if err != nil {
					t.Fatalf("Append KindStepStarted: %v", err)
				}
				ok, err := b.jrnl.MarkTerminal(ctx, idutil.SafeID(orderID), leaseID, saga.StatusSucceeded)
				if err != nil {
					t.Fatalf("MarkTerminal Succeeded: %v", err)
				}
				if !ok {
					t.Fatal("MarkTerminal Succeeded: returned ok=false")
				}
			},
			wantStatus: orderstatus.StatusSucceeded,
		},
		{
			name: "compensated",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Running (step event), then Running → Compensating,
				// then Compensating → Compensated (terminal).
				_, err := b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind:     journal.KindStepStarted,
					StepName: "reserve",
				})
				if err != nil {
					t.Fatalf("Append KindStepStarted: %v", err)
				}
				_, err = b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind: journal.KindCompensationStarted,
				})
				if err != nil {
					t.Fatalf("Append KindCompensationStarted: %v", err)
				}
				ok, err := b.jrnl.MarkTerminal(ctx, idutil.SafeID(orderID), leaseID, saga.StatusCompensated)
				if err != nil {
					t.Fatalf("MarkTerminal Compensated: %v", err)
				}
				if !ok {
					t.Fatal("MarkTerminal Compensated: returned ok=false")
				}
			},
			wantStatus: orderstatus.StatusCompensated,
		},
		{
			name: "failed_via_saga_failed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Failed directly (no steps committed, no compensation).
				ok, err := b.jrnl.MarkTerminal(ctx, idutil.SafeID(orderID), leaseID, saga.StatusFailed)
				if err != nil {
					t.Fatalf("MarkTerminal Failed: %v", err)
				}
				if !ok {
					t.Fatal("MarkTerminal Failed: returned ok=false")
				}
			},
			wantStatus: orderstatus.StatusFailed,
		},
		{
			name: "failed_via_saga_expired",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				ok, err := b.jrnl.MarkTerminal(ctx, idutil.SafeID(orderID), leaseID, saga.StatusExpired)
				if err != nil {
					t.Fatalf("MarkTerminal Expired: %v", err)
				}
				if !ok {
					t.Fatal("MarkTerminal Expired: returned ok=false")
				}
			},
			wantStatus: orderstatus.StatusFailed,
		},
		{
			name: "failed_via_saga_compensation_failed",
			setup: func(t *testing.T, b testBundle, orderID string) {
				t.Helper()
				ctx := context.Background()
				leaseID := claimLease(t, b.jrnl)
				// Pending → Running → Compensating → CompensationFailed.
				_, err := b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind:     journal.KindStepStarted,
					StepName: "reserve",
				})
				if err != nil {
					t.Fatalf("Append KindStepStarted: %v", err)
				}
				_, err = b.jrnl.Append(ctx, idutil.SafeID(orderID), leaseID, journal.Event{
					Kind: journal.KindCompensationStarted,
				})
				if err != nil {
					t.Fatalf("Append KindCompensationStarted: %v", err)
				}
				ok, err := b.jrnl.MarkTerminal(ctx, idutil.SafeID(orderID), leaseID, saga.StatusCompensationFailed)
				if err != nil {
					t.Fatalf("MarkTerminal CompensationFailed: %v", err)
				}
				if !ok {
					t.Fatal("MarkTerminal CompensationFailed: returned ok=false")
				}
			},
			wantStatus: orderstatus.StatusFailed,
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
