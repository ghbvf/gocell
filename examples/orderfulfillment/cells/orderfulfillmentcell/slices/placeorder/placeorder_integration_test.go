//go:build integration

// Package placeorder_test provides end-to-end integration tests for the
// placeorder slice exercising the full saga coordinator wiring with real
// in-memory stores. These tests verify L3 terminal-state acceptance criteria.
package placeorder_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/saga"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
	saga "github.com/ghbvf/gocell/runtime/saga"
)

// setup builds the full coordinator + placeorder wiring with a shared MemJournal.
// It returns the placeorder service, the shared journal (for Load), the memory
// stores (for side-effect assertions), and a cancel func that stops the coordinator.
type testSetup struct {
	svc       *placeorder.Service
	jrnl      *journal.MemJournal
	inventory *mem.InventoryStore
	payments  *mem.PaymentStore
	shipments *mem.ShipmentStore
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

	impl := sagaimpl.NewImpl(orders, inv, pay, ship)
	reg, err := of.Register(impl)
	if err != nil {
		t.Fatalf("of.Register: %v", err)
	}

	cfg := saga.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	// Short LeaseDuration so the coordinator can re-claim the same instance on
	// every tick (each step completes in < 50ms; the lease expires quickly so
	// the next tick sees the instance as available again). The constraint
	// HeartbeatInterval * HeartbeatLeaseSafetyFactor(2) < LeaseDuration is
	// satisfied: 50ms*2 = 100ms < 200ms.
	cfg.LeaseDuration = 200 * time.Millisecond
	cfg.HeartbeatInterval = 50 * time.Millisecond

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

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if startErr := coord.Start(ctx); startErr != nil && ctx.Err() == nil {
			t.Logf("coord.Start returned: %v", startErr)
		}
	}()
	<-coord.Ready()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	}
}

// waitTerminal polls the journal until a terminal event is observed for id or
// the deadline passes. Returns the first terminal EventKind found.
func waitTerminal(t *testing.T, j *journal.MemJournal, id idutil.SafeID, deadline time.Time) journal.EventKind {
	t.Helper()
	ctx := context.Background()
	for time.Now().Before(deadline) {
		events, err := j.Load(ctx, id)
		if err != nil {
			t.Fatalf("waitTerminal: Load: %v", err)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind.IsTerminal() {
				return events[i].Kind
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitTerminal: no terminal event within deadline for id=%s", id)
	return 0
}

// TestPlaceOrder_HappyPath verifies a successful saga run:
// - terminal state is KindSagaSucceeded
// - inventory is decremented (reservation held)
// - payment is recorded
// - shipment is recorded
func TestPlaceOrder_HappyPath(t *testing.T) {
	ts := setup(t)
	ctx := context.Background()

	orderID, err := ts.svc.PlaceOrder(ctx, "widget", 1299, false)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	kind := waitTerminal(t, ts.jrnl, idutil.SafeID(orderID), time.Now().Add(5*time.Second))
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
	ctx := context.Background()

	orderID, err := ts.svc.PlaceOrder(ctx, "widget", 1299, true)
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	kind := waitTerminal(t, ts.jrnl, idutil.SafeID(orderID), time.Now().Add(5*time.Second))
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
