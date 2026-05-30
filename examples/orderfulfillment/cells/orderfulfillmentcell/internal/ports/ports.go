// Package ports defines the repository and store interfaces for the ordercell.
// Implementations live in the mem/ sub-package.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
)

// OrderRepository persists and retrieves Order aggregates.
type OrderRepository interface {
	// Create stores a new order. Returns an error if the store fails.
	Create(ctx context.Context, order *domain.Order) error
	// GetByID retrieves an order by its ID.
	// Returns errcode.KindNotFound / errcode.ErrOrderNotFound when absent.
	GetByID(ctx context.Context, id string) (*domain.Order, error)
}

// InventoryStore manages inventory reservations.
type InventoryStore interface {
	// Reserve decrements available count for item and records the reservation.
	// Returns a deterministic reservationID and an error if the item is
	// unknown or out of stock (errcode.KindFailedPrecondition).
	Reserve(ctx context.Context, orderID, item string) (reservationID string, err error)
	// Release re-increments the available count for the item reserved by orderID.
	// Idempotent: releasing an unknown or already-released orderID is a no-op
	// returning nil.
	Release(ctx context.Context, orderID string) error
	// Available returns the current available count for item.
	// Intended for test assertions; no ctx needed.
	Available(item string) int
}

// PaymentStore manages payment charges and refunds.
type PaymentStore interface {
	// Charge creates a payment record and returns a deterministic paymentID.
	Charge(ctx context.Context, orderID string, amountCents int64) (paymentID string, err error)
	// Refund voids the charge for orderID.
	// Idempotent: refunding an unknown or already-refunded orderID returns nil.
	Refund(ctx context.Context, orderID string) error
	// Get returns the paymentID for orderID if a charge exists.
	// Intended for test assertions.
	Get(orderID string) (paymentID string, ok bool)
}

// ShipmentStore manages shipment creation and cancellation.
type ShipmentStore interface {
	// CreateShipment creates a shipment for orderID and returns a deterministic
	// shipmentID.
	CreateShipment(ctx context.Context, orderID string) (shipmentID string, err error)
	// CancelShipment cancels the shipment for orderID.
	// Idempotent: canceling an unknown or already-canceled orderID returns nil.
	CancelShipment(ctx context.Context, orderID string) error
	// Get returns the shipmentID for orderID if a shipment exists.
	// Intended for test assertions.
	Get(orderID string) (shipmentID string, ok bool)
}
