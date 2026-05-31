// Package sagaimpl implements the orderfulfillment saga business logic.
// It bridges the generated of.Impl interface to the domain ports.
package sagaimpl

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/ports"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Verify that Impl satisfies the generated contract interface at compile time.
var _ of.Impl = (*Impl)(nil)

// Impl implements of.Impl by delegating to the domain port interfaces.
type Impl struct {
	orders    ports.OrderRepository
	inventory ports.InventoryStore
	payments  ports.PaymentStore
	shipments ports.ShipmentStore
}

// NewImpl constructs an Impl with all four required ports.
// Returns an error if any dependency is nil.
func NewImpl(
	orders ports.OrderRepository,
	inventory ports.InventoryStore,
	payments ports.PaymentStore,
	shipments ports.ShipmentStore,
) (*Impl, error) {
	if validation.IsNilInterface(orders) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"orderfulfillment saga impl: orders repository required")
	}
	if validation.IsNilInterface(inventory) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"orderfulfillment saga impl: inventory store required")
	}
	if validation.IsNilInterface(payments) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"orderfulfillment saga impl: payment store required")
	}
	if validation.IsNilInterface(shipments) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"orderfulfillment saga impl: shipment store required")
	}
	return &Impl{
		orders:    orders,
		inventory: inventory,
		payments:  payments,
		shipments: shipments,
	}, nil
}

// RunReserveInventory loads the order and reserves inventory.
func (s *Impl) RunReserveInventory(ctx context.Context, inst *ksaga.Instance) (of.ReserveInventoryOutput, error) {
	order, err := s.orders.GetByID(ctx, string(inst.ID))
	if err != nil {
		return of.ReserveInventoryOutput{}, fmt.Errorf("reserveInventory: load order: %w", err)
	}
	resID, err := s.inventory.Reserve(ctx, string(inst.ID), order.Item)
	if err != nil {
		return of.ReserveInventoryOutput{}, fmt.Errorf("reserveInventory: reserve: %w", err)
	}
	return of.ReserveInventoryOutput{ReservationID: resID}, nil
}

// CompensateReserveInventory releases the inventory reservation.
// PURE: calls only s.inventory.Release — no persistence or outbox calls.
func (s *Impl) CompensateReserveInventory(ctx context.Context, inst *ksaga.Instance, _ of.ReserveInventoryOutput) error {
	return s.inventory.Release(ctx, string(inst.ID))
}

// RunChargePayment loads the order and charges the payment.
// Returns errcode.KindConflict / ErrConflict when order.PaymentShouldFail is
// set, triggering saga compensation.
func (s *Impl) RunChargePayment(ctx context.Context, inst *ksaga.Instance, _ of.ReserveInventoryOutput) (of.ChargePaymentOutput, error) {
	order, err := s.orders.GetByID(ctx, string(inst.ID))
	if err != nil {
		return of.ChargePaymentOutput{}, fmt.Errorf("chargePayment: load order: %w", err)
	}
	if order.PaymentShouldFail {
		return of.ChargePaymentOutput{},
			errcode.New(errcode.KindConflict, errcode.ErrConflict, "payment declined")
	}
	paymentID, err := s.payments.Charge(ctx, string(inst.ID), order.AmountCents)
	if err != nil {
		return of.ChargePaymentOutput{}, fmt.Errorf("chargePayment: charge: %w", err)
	}
	return of.ChargePaymentOutput{PaymentID: paymentID, AmountCents: order.AmountCents}, nil
}

// CompensateChargePayment refunds the payment.
// PURE: calls only s.payments.Refund — no persistence or outbox calls.
func (s *Impl) CompensateChargePayment(ctx context.Context, inst *ksaga.Instance, _ of.ChargePaymentOutput) error {
	return s.payments.Refund(ctx, string(inst.ID))
}

// RunShip creates a shipment for the order.
func (s *Impl) RunShip(ctx context.Context, inst *ksaga.Instance, _ of.ChargePaymentOutput) (of.ShipOutput, error) {
	shipmentID, err := s.shipments.CreateShipment(ctx, string(inst.ID))
	if err != nil {
		return of.ShipOutput{}, fmt.Errorf("ship: create shipment: %w", err)
	}
	return of.ShipOutput{ShipmentID: shipmentID}, nil
}

// CompensateShip cancels the shipment.
// PURE: calls only s.shipments.CancelShipment — no persistence or outbox calls.
func (s *Impl) CompensateShip(ctx context.Context, inst *ksaga.Instance, _ of.ShipOutput) error {
	return s.shipments.CancelShipment(ctx, string(inst.ID))
}

// RunNotifyUser is the terminal step; it always succeeds with Notified: true.
// There is no CompensateNotifyUser — notifications are best-effort.
func (s *Impl) RunNotifyUser(_ context.Context, _ *ksaga.Instance, _ of.ShipOutput) (of.NotifyUserOutput, error) {
	return of.NotifyUserOutput{Notified: true}, nil
}
