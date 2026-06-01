//go:build integration

// Package placeorder_test provides end-to-end integration tests for the
// placeorder slice exercising the full saga coordinator wiring with real
// in-memory stores. These tests verify L3 terminal-state acceptance criteria.
package placeorder_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/ports"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	shipments ports.ShipmentStore
	coord     *saga.Coordinator
	ctx       context.Context
}

func setup(t *testing.T) testSetup {
	t.Helper()
	return setupWith(t, mem.NewShipmentStore())
}

// setupWith builds the full coordinator + placeorder wiring with a
// caller-supplied ShipmentStore. Tests inject a failing store (see
// failingShipmentStore) to drive the multi-step reverse compensation path,
// where ship fails after reserveInventory and chargePayment have completed.
func setupWith(t *testing.T, ship ports.ShipmentStore) testSetup {
	t.Helper()

	clk := clock.Real()

	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		t.Fatalf("NewMemJournal: %v", err)
	}

	orders := mem.NewOrderRepository()
	inv := mem.NewInventoryStore(map[string]int{"widget": 100})
	pay := mem.NewPaymentStore()

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
	// LeaseDuration set to 2s to give slow CI runners enough headroom.
	// HeartbeatInterval * HeartbeatLeaseSafetyFactor(2) < LeaseDuration is
	// satisfied: 50ms*2 = 100ms << 2s.
	cfg.LeaseDuration = testtime.D2s
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
	}, testtime.EventuallyExtraLong, testtime.FastPoll)
	return found
}

// stepNamesOfKind returns the StepName of every event matching kind, in order.
func stepNamesOfKind(events []journal.Event, kind journal.EventKind) []string {
	var names []string
	for _, ev := range events {
		if ev.Kind == kind {
			names = append(names, string(ev.StepName))
		}
	}
	return names
}

// containsKind reports whether any event has the given kind.
func containsKind(events []journal.Event, kind journal.EventKind) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// requireStepSequence asserts got equals want (order-sensitive), labeling
// failures with label.
func requireStepSequence(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v (len %d), want %v (len %d)", label, got, len(got), want, len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", label, i, got[i], want[i])
		}
	}
}

// TestPlaceOrder_HappyPath verifies a successful saga run:
// - terminal state is KindSagaSucceeded
// - inventory is decremented (reservation held)
// - payment is recorded
// - shipment is recorded
func TestPlaceOrder_HappyPath(t *testing.T) {
	t.Parallel()
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "int-happy-1", "widget", 1299, false)
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
	t.Parallel()
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "int-comp-1", "widget", 1299, true)
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
	t.Parallel()
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "int-evtseq-1", "widget", 1299, false)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))

	events, err := ts.jrnl.Load(ts.ctx, idutil.SafeID(orderID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	requireStepSequence(t, "completed steps",
		stepNamesOfKind(events, journal.KindStepCompleted),
		[]string{"reserveInventory", "chargePayment", "ship", "notifyUser"})

	// No compensation events on the happy path.
	if containsKind(events, journal.KindStepCompensated) || containsKind(events, journal.KindCompensationStarted) {
		t.Error("unexpected compensation event on happy path")
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
	t.Parallel()
	ts := setup(t)

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "int-compseq-1", "widget", 1299, true)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))

	events, err := ts.jrnl.Load(ts.ctx, idutil.SafeID(orderID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// When shouldCompensate=true, the coordinator does NOT write KindStepFailed
	// for chargePayment; it writes KindCompensationStarted and begins rollback.
	completed := stepNamesOfKind(events, journal.KindStepCompleted)
	compensated := stepNamesOfKind(events, journal.KindStepCompensated)

	if slices.Contains(completed, "chargePayment") {
		t.Error("chargePayment must not complete when paymentShouldFail=true")
	}
	if !containsKind(events, journal.KindCompensationStarted) {
		t.Error("expected KindCompensationStarted event for chargePayment failure")
	}

	// Only reserveInventory is compensated.
	requireStepSequence(t, "compensated steps", compensated, []string{"reserveInventory"})

	// ship and notifyUser are never completed or compensated.
	for _, name := range []string{"ship", "notifyUser"} {
		if slices.Contains(completed, name) || slices.Contains(compensated, name) {
			t.Errorf("unexpected completed/compensated event for step %q after chargePayment failure", name)
		}
	}
}

// failingShipmentStore wraps a ShipmentStore but always fails CreateShipment.
// It drives the ship-step failure that exercises the multi-step reverse
// compensation path: reserveInventory and chargePayment have already completed,
// so the coordinator must compensate them in reverse order
// (chargePayment → reserveInventory). CancelShipment / Get delegate to the
// embedded store so compensation and assertions behave normally.
type failingShipmentStore struct {
	ports.ShipmentStore
}

func (failingShipmentStore) CreateShipment(context.Context, string) (string, error) {
	// KindConflict mirrors the payment-decline classification so the coordinator
	// treats it as non-retryable and enters compensation rather than retrying.
	return "", errcode.New(errcode.KindConflict, errcode.ErrConflict, "shipment carrier rejected")
}

// TestPlaceOrder_CompensateOnShipFail_EventSequence verifies the deeper
// compensation path than chargePayment failure: when ship fails after
// reserveInventory and chargePayment have completed, the coordinator compensates
// BOTH prior steps in reverse order (chargePayment, then reserveInventory),
// refunds the payment, releases the inventory, and reaches terminal
// KindSagaCompensated. paymentShouldFail is false here — payment succeeds and is
// then rolled back, which the chargePayment-failure case never exercises.
// F5: multi-step reverse-order compensation coverage.
func TestPlaceOrder_CompensateOnShipFail_EventSequence(t *testing.T) {
	t.Parallel()
	ts := setupWith(t, failingShipmentStore{mem.NewShipmentStore()})

	orderID, err := ts.svc.PlaceOrder(ts.ctx, "int-shipcomp-1", "widget", 1299, false)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	kind := waitTerminal(t, ts.ctx, ts.jrnl, idutil.SafeID(orderID))
	if kind != journal.KindSagaCompensated {
		t.Errorf("terminal kind = %s, want %s", kind, journal.KindSagaCompensated)
	}

	events, err := ts.jrnl.Load(ts.ctx, idutil.SafeID(orderID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// reserveInventory and chargePayment must have completed before ship failed.
	requireStepSequence(t, "completed steps",
		stepNamesOfKind(events, journal.KindStepCompleted),
		[]string{"reserveInventory", "chargePayment"})

	// Compensation must run BOTH completed steps in reverse order.
	requireStepSequence(t, "compensated steps",
		stepNamesOfKind(events, journal.KindStepCompensated),
		[]string{"chargePayment", "reserveInventory"})

	// Side effects fully rolled back: inventory restored, payment refunded,
	// no shipment recorded.
	if got := ts.inventory.Available("widget"); got != 100 {
		t.Errorf("inventory.Available(widget) = %d after compensation, want 100", got)
	}
	if _, ok := ts.payments.Get(orderID); ok {
		t.Errorf("payment should be refunded after compensation, but was still present for orderID=%s", orderID)
	}
	if _, ok := ts.shipments.Get(orderID); ok {
		t.Errorf("shipment should not exist after ship failure, but was present for orderID=%s", orderID)
	}
}
