//go:build integration

// Package placeorder_test provides end-to-end integration tests for the
// placeorder slice exercising the full saga coordinator wiring with real
// in-memory stores. These tests verify L3 terminal-state acceptance criteria.
package placeorder_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	saga "github.com/ghbvf/gocell/runtime/saga"
)

// setup builds the full coordinator + placeorder wiring with a shared MemJournal.
// It returns the placeorder service, the shared journal (for Load), the memory
// stores (for side-effect assertions), the coordinator (for liveness inspection),
// and a test context that is canceled on t.Cleanup.
type testSetup struct {
	svc       *placeorder.Service
	jrnl      *journal.MemJournal
	inventory *mem.InventoryStore
	payments  *mem.PaymentStore
	shipments *mem.ShipmentStore
	coord     *saga.Coordinator
	ctx       context.Context
}

func setup(t *testing.T) testSetup {
	t.Helper()

	clk := clock.Real()

	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	orders := mem.NewOrderRepository()
	inv := mem.NewInventoryStore(map[string]int{"widget": 100})
	pay := mem.NewPaymentStore()
	ship := mem.NewShipmentStore()

	impl, err := sagaimpl.NewImpl(orders, inv, pay, ship)
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	reg, err := of.Register(impl)
	if err != nil {
		t.Fatalf("of.Register: %v", err)
	}

	cfg := saga.DefaultConfig()
	cfg.PollInterval = testtime.D20ms
	// Short LeaseDuration so the coordinator can re-claim the same instance on
	// every tick (each step completes in < 50ms; the lease expires quickly so
	// the next tick sees the instance as available again). The constraint
	// HeartbeatInterval * HeartbeatLeaseSafetyFactor(2) < LeaseDuration is
	// satisfied: 50ms*2 = 100ms < 200ms.
	cfg.LeaseDuration = testtime.D200ms
	cfg.HeartbeatInterval = testtime.D50ms

	coord, err := saga.NewCoordinator(
		jrnl,
		koutbox.DemoTxRunner{},
		koutbox.NewNoopEmitter(),
		reg,
		clk,
		saga.WithConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ownerCtx, cancel := context.WithCancel(context.Background())
	go func() {
		if startErr := coord.Start(ownerCtx); startErr != nil && ownerCtx.Err() == nil {
			t.Logf("coord.Start returned: %v", startErr)
		}
	}()
	<-coord.Ready()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.SelectShutdown)
		defer stopCancel()
		_ = coord.Stop(stopCtx)
		cancel()
	})

	svc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(orders),
		placeorder.WithJournal(jrnl),
		placeorder.WithLogger(slog.Default()),
	)
	if err != nil {
		t.Fatalf("placeorder.NewService: %v", err)
	}

	return testSetup{
		svc:       svc,
		jrnl:      jrnl,
		inventory: inv,
		payments:  pay,
		shipments: ship,
		coord:     coord,
		ctx:       ownerCtx,
	}
}

// waitTerminal polls the journal until a terminal event is observed for id or
// the parent context is canceled. Returns the first terminal EventKind found.
// Uses testwait.External to satisfy TEST-SLEEP-DISCIPLINE-01 and TEST-TIME-LITERAL-01.
func waitTerminal(t *testing.T, ctx context.Context, j *journal.MemJournal, id idutil.SafeID) journal.EventKind {
	t.Helper()
	var found journal.EventKind
	testwait.External(t, "saga-terminal-state", func() bool {
		if ctx.Err() != nil {
			return true // abort on context cancellation
		}
		events, err := j.Load(ctx, id)
		if err != nil {
			return false
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind.IsTerminal() {
				found = events[i].Kind
				return true
			}
		}
		return false
	}, testtime.EventuallyLong, testtime.FastPoll)
	return found
}

// TestPlaceOrder_HappyPath verifies a successful saga run:
// - terminal state is KindSagaSucceeded
// - inventory is decremented (reservation held)
// - payment is recorded
// - shipment is recorded
func TestPlaceOrder_HappyPath(t *testing.T) {
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "widget", 1299, false)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	kind := waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))
	if kind != journal.KindSagaSucceeded {
		t.Errorf("terminal kind = %s, want %s", kind, journal.KindSagaSucceeded)
	}

	if got := ts.inventory.Available("widget"); got != 99 {
		t.Errorf("inventory.Available(widget) = %d, want 99", got)
	}

	if _, ok := ts.payments.Get(orderID); !ok {
		t.Errorf("payment not recorded for orderID=%s", orderID)
	}

	if _, ok := ts.shipments.Get(orderID); !ok {
		t.Errorf("shipment not recorded for orderID=%s", orderID)
	}
}

// TestPlaceOrder_CompensateOnChargeFail verifies compensation when payment fails:
// - terminal state is KindSagaCompensated
// - inventory is released back to 100
// - no payment recorded
// - no shipment recorded
func TestPlaceOrder_CompensateOnChargeFail(t *testing.T) {
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "widget", 1299, true)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	kind := waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))
	if kind != journal.KindSagaCompensated {
		t.Errorf("terminal kind = %s, want %s", kind, journal.KindSagaCompensated)
	}

	if got := ts.inventory.Available("widget"); got != 100 {
		t.Errorf("inventory.Available(widget) = %d after compensation, want 100", got)
	}

	if _, ok := ts.payments.Get(orderID); ok {
		t.Errorf("payment should not be recorded after compensation, but was for orderID=%s", orderID)
	}

	if _, ok := ts.shipments.Get(orderID); ok {
		t.Errorf("shipment should not be recorded after compensation, but was for orderID=%s", orderID)
	}
}

// TestPlaceOrder_HappyPath_EventSequence asserts the full journal event sequence
// for the happy path: four forward steps complete in order, terminal is Succeeded.
// F4: event sequence assertion for saga orchestration.
func TestPlaceOrder_HappyPath_EventSequence(t *testing.T) {
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "widget", 1299, false)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))

	events, err := ts.jrnl.Load(ts.ctx, idutil.SafeID(orderID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Extract the step names from KindStepCompleted events in order.
	var completedSteps []string
	for _, ev := range events {
		if ev.Kind == journal.KindStepCompleted {
			completedSteps = append(completedSteps, string(ev.StepName))
		}
	}

	wantSteps := []string{"reserveInventory", "chargePayment", "ship", "notifyUser"}
	if len(completedSteps) != len(wantSteps) {
		t.Errorf("completed steps = %v (len %d), want %v (len %d)",
			completedSteps, len(completedSteps), wantSteps, len(wantSteps))
	} else {
		for i, want := range wantSteps {
			if completedSteps[i] != want {
				t.Errorf("step[%d] = %q, want %q", i, completedSteps[i], want)
			}
		}
	}

	// No compensation events on the happy path.
	for _, ev := range events {
		if ev.Kind == journal.KindStepCompensated || ev.Kind == journal.KindCompensationStarted {
			t.Errorf("unexpected compensation event kind=%s step=%s on happy path", ev.Kind, ev.StepName)
		}
	}
}

// TestPlaceOrder_CompensateOnChargeFail_EventSequence asserts the journal event
// sequence when chargePayment fails: KindCompensationStarted is written (the
// coordinator transitions to Compensating without writing KindStepFailed — see
// coordinator.go routeOutcome: shouldCompensate branch skips commitStepFailed),
// then only CompensateReserveInventory fires, and the saga reaches terminal
// KindSagaCompensated.
// F4: compensation sequence assertion.
func TestPlaceOrder_CompensateOnChargeFail_EventSequence(t *testing.T) {
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "widget", 1299, true)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))

	events, err := ts.jrnl.Load(ts.ctx, idutil.SafeID(orderID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// When shouldCompensate=true, coordinator does NOT write KindStepFailed for
	// chargePayment. Instead it writes KindCompensationStarted and begins rollback.
	// Verify chargePayment was never completed.
	for _, ev := range events {
		if string(ev.StepName) == "chargePayment" && ev.Kind == journal.KindStepCompleted {
			t.Error("chargePayment should not have a KindStepCompleted event when paymentShouldFail=true")
		}
	}

	// KindCompensationStarted must appear (coordinator signals rollback entry).
	var sawCompensationStarted bool
	for _, ev := range events {
		if ev.Kind == journal.KindCompensationStarted {
			sawCompensationStarted = true
		}
	}
	if !sawCompensationStarted {
		t.Error("expected KindCompensationStarted event in journal for chargePayment failure")
	}

	// Verify compensate sequence: only reserveInventory was compensated.
	var compensatedSteps []string
	for _, ev := range events {
		if ev.Kind == journal.KindStepCompensated {
			compensatedSteps = append(compensatedSteps, string(ev.StepName))
		}
	}
	if len(compensatedSteps) != 1 || compensatedSteps[0] != "reserveInventory" {
		t.Errorf("compensated steps = %v, want [reserveInventory]", compensatedSteps)
	}

	// ship and notifyUser must have no completed or compensated events.
	for _, ev := range events {
		name := string(ev.StepName)
		if (name == "ship" || name == "notifyUser") &&
			(ev.Kind == journal.KindStepCompleted || ev.Kind == journal.KindStepCompensated) {
			t.Errorf("unexpected event kind=%s for step=%s after chargePayment failure", ev.Kind, name)
		}
	}
}

