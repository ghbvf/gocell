package sagaimpl_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/ports"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/sagaimpl"
	ksaga "github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
)

// failingShipmentStore is a ports.ShipmentStore stub whose CreateShipment
// always returns the configured error.
type failingShipmentStore struct {
	createErr error
}

func (f *failingShipmentStore) CreateShipment(_ context.Context, _ string) (string, error) {
	return "", f.createErr
}

func (f *failingShipmentStore) CancelShipment(_ context.Context, _ string) error {
	return nil
}

func (f *failingShipmentStore) Get(_ string) (string, bool) {
	return "", false
}

// Verify that failingShipmentStore satisfies the interface at compile time.
var _ ports.ShipmentStore = (*failingShipmentStore)(nil)

// newTestImpl builds a saga Impl with pre-seeded inventory.
func newTestImpl(t *testing.T, stock map[string]int) (
	*sagaimpl.Impl,
	*mem.OrderRepository,
	*mem.InventoryStore,
	*mem.PaymentStore,
	*mem.ShipmentStore,
) {
	t.Helper()
	orders := mem.NewOrderRepository()
	inventory := mem.NewInventoryStore(stock)
	payments := mem.NewPaymentStore()
	shipments := mem.NewShipmentStore()
	impl, err := sagaimpl.NewImpl(orders, inventory, payments, shipments)
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}
	return impl, orders, inventory, payments, shipments
}

// newInst creates a saga Instance for testing.
func newInst(id string) *ksaga.Instance {
	inst := ksaga.NewInstance(idutil.SafeID(id), of.DefinitionID, time.Now())
	return &inst
}

// seedOrder seeds an order into the repository.
func seedOrder(t *testing.T, r *mem.OrderRepository, o *domain.Order) {
	t.Helper()
	if err := r.Create(context.Background(), o); err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func TestRunReserveInventory_Happy(t *testing.T) {
	t.Parallel()
	impl, orders, inventory, _, _ := newTestImpl(t, map[string]int{"widget": 3})
	inst := newInst("ord-test")
	seedOrder(t, orders, &domain.Order{ID: "ord-test", Item: "widget", AmountCents: 1000, CreatedAt: time.Now()})

	out, err := impl.RunReserveInventory(context.Background(), inst)
	if err != nil {
		t.Fatalf("RunReserveInventory: %v", err)
	}
	if out.ReservationID != "res-ord-test" {
		t.Errorf("ReservationID = %q, want res-ord-test", out.ReservationID)
	}
	if inventory.Available("widget") != 2 {
		t.Errorf("available after reserve = %d, want 2", inventory.Available("widget"))
	}
}

func TestRunReserveInventory_OrderNotFound(t *testing.T) {
	t.Parallel()
	impl, _, _, _, _ := newTestImpl(t, map[string]int{"widget": 1})
	inst := newInst("nonexistent")

	_, err := impl.RunReserveInventory(context.Background(), inst)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound, got %v", err)
	}
}

func TestCompensateReserveInventory(t *testing.T) {
	t.Parallel()
	impl, orders, inventory, _, _ := newTestImpl(t, map[string]int{"widget": 1})
	inst := newInst("ord-comp")
	seedOrder(t, orders, &domain.Order{ID: "ord-comp", Item: "widget", AmountCents: 500, CreatedAt: time.Now()})

	// first reserve
	out, err := impl.RunReserveInventory(context.Background(), inst)
	if err != nil {
		t.Fatalf("RunReserveInventory: %v", err)
	}
	if inventory.Available("widget") != 0 {
		t.Errorf("available should be 0 after reserve")
	}

	// compensate
	if err := impl.CompensateReserveInventory(context.Background(), inst, out); err != nil {
		t.Fatalf("CompensateReserveInventory: %v", err)
	}
	if inventory.Available("widget") != 1 {
		t.Errorf("available after compensation = %d, want 1", inventory.Available("widget"))
	}
}

func TestRunChargePayment_Happy(t *testing.T) {
	t.Parallel()
	impl, orders, _, payments, _ := newTestImpl(t, map[string]int{"widget": 1})
	inst := newInst("ord-pay")
	seedOrder(t, orders, &domain.Order{ID: "ord-pay", Item: "widget", AmountCents: 2500, CreatedAt: time.Now()})
	prevOut := of.ReserveInventoryOutput{ReservationID: "res-ord-pay"}

	out, err := impl.RunChargePayment(context.Background(), inst, prevOut)
	if err != nil {
		t.Fatalf("RunChargePayment: %v", err)
	}
	if out.PaymentID != "pay-ord-pay" {
		t.Errorf("PaymentID = %q, want pay-ord-pay", out.PaymentID)
	}
	if out.AmountCents != 2500 {
		t.Errorf("AmountCents = %d, want 2500", out.AmountCents)
	}
	_, ok := payments.Get("ord-pay")
	if !ok {
		t.Error("payment not recorded in store")
	}
}

func TestRunChargePayment_Declined(t *testing.T) {
	t.Parallel()
	impl, orders, _, _, _ := newTestImpl(t, map[string]int{"widget": 1})
	inst := newInst("ord-declined")
	seedOrder(t, orders, &domain.Order{
		ID:                "ord-declined",
		Item:              "widget",
		AmountCents:       100,
		PaymentShouldFail: true,
		CreatedAt:         time.Now(),
	})
	prevOut := of.ReserveInventoryOutput{ReservationID: "res-ord-declined"}

	_, err := impl.RunChargePayment(context.Background(), inst, prevOut)
	if err == nil {
		t.Fatal("expected error for declined payment, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindConflict {
		t.Errorf("expected KindConflict, got %v", err)
	}
}

func TestCompensateChargePayment(t *testing.T) {
	t.Parallel()
	impl, orders, _, payments, _ := newTestImpl(t, map[string]int{"widget": 1})
	inst := newInst("ord-refund")
	seedOrder(t, orders, &domain.Order{ID: "ord-refund", Item: "widget", AmountCents: 300, CreatedAt: time.Now()})
	prevOut := of.ReserveInventoryOutput{ReservationID: "res-ord-refund"}

	chargeOut, err := impl.RunChargePayment(context.Background(), inst, prevOut)
	if err != nil {
		t.Fatalf("RunChargePayment: %v", err)
	}

	if err := impl.CompensateChargePayment(context.Background(), inst, chargeOut); err != nil {
		t.Fatalf("CompensateChargePayment: %v", err)
	}
	_, ok := payments.Get("ord-refund")
	if ok {
		t.Error("payment still present after compensation")
	}
}

func TestRunShip_Happy(t *testing.T) {
	t.Parallel()
	impl, _, _, _, shipments := newTestImpl(t, map[string]int{})
	inst := newInst("ord-ship")
	prevOut := of.ChargePaymentOutput{PaymentID: "pay-ord-ship", AmountCents: 1000}

	out, err := impl.RunShip(context.Background(), inst, prevOut)
	if err != nil {
		t.Fatalf("RunShip: %v", err)
	}
	if out.ShipmentID != "ship-ord-ship" {
		t.Errorf("ShipmentID = %q, want ship-ord-ship", out.ShipmentID)
	}
	_, ok := shipments.Get("ord-ship")
	if !ok {
		t.Error("shipment not recorded in store")
	}
}

func TestCompensateShip(t *testing.T) {
	t.Parallel()
	impl, _, _, _, shipments := newTestImpl(t, map[string]int{})
	inst := newInst("ord-ship-comp")
	prevOut := of.ChargePaymentOutput{PaymentID: "pay-ord-ship-comp", AmountCents: 500}

	shipOut, err := impl.RunShip(context.Background(), inst, prevOut)
	if err != nil {
		t.Fatalf("RunShip: %v", err)
	}

	if err := impl.CompensateShip(context.Background(), inst, shipOut); err != nil {
		t.Fatalf("CompensateShip: %v", err)
	}
	_, ok := shipments.Get("ord-ship-comp")
	if ok {
		t.Error("shipment still present after compensation")
	}
}

func TestRunNotifyUser(t *testing.T) {
	t.Parallel()
	impl, _, _, _, _ := newTestImpl(t, map[string]int{})
	inst := newInst("ord-notify")
	prevOut := of.ShipOutput{ShipmentID: "ship-ord-notify"}

	out, err := impl.RunNotifyUser(context.Background(), inst, prevOut)
	if err != nil {
		t.Fatalf("RunNotifyUser: %v", err)
	}
	if !out.Notified {
		t.Error("Notified should be true")
	}
}

func TestRunShip_ShipmentFail(t *testing.T) {
	t.Parallel()

	orders := mem.NewOrderRepository()
	inventory := mem.NewInventoryStore(map[string]int{})
	payments := mem.NewPaymentStore()
	shipStore := &failingShipmentStore{createErr: fmt.Errorf("backend unavailable")}

	impl, err := sagaimpl.NewImpl(orders, inventory, payments, shipStore)
	if err != nil {
		t.Fatalf("NewImpl: %v", err)
	}

	inst := newInst("ord-shipfail")
	prevOut := of.ChargePaymentOutput{PaymentID: "pay-ord-shipfail", AmountCents: 100}

	_, gotErr := impl.RunShip(context.Background(), inst, prevOut)
	if gotErr == nil {
		t.Fatal("expected error from RunShip when CreateShipment fails, got nil")
	}
	// RunShip wraps with "ship: create shipment: <err>"
	if !errors.Is(gotErr, shipStore.createErr) {
		t.Errorf("error chain does not contain original error; got: %v", gotErr)
	}
}
