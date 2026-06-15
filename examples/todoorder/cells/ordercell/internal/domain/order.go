// Package domain defines the core domain model for the ordercell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
)

// Order status constants.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
)

// Order represents a todoorder aggregate.
type Order struct {
	ID string
	// Owner is the JWT subject of the user who created this order.
	// Consumed by the PDP ownership rule (orderAuthorizer); server-derived and
	// intentionally off-wire (not included in HTTP responses or event payloads).
	Owner     string
	Item      string
	Status    string // pending, confirmed
	CreatedAt time.Time
}

// Confirm transitions the order from pending to confirmed.
// Returns an error if the order is not in the pending state.
func (o *Order) Confirm() error {
	if o.Status != StatusPending {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"order: only pending orders can be confirmed")
	}
	o.Status = StatusConfirmed
	return nil
}

// OrderRepository abstracts order persistence.
type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	GetByID(ctx context.Context, id string) (*Order, error)
	// List returns the orders owned by owner (Order.Owner == owner), paginated per
	// params. Owner is applied at the data source so list is owner-scoped: the route
	// gate (RequirePermission(order:list)) is coarse, so without this filter a customer
	// would page over every owner's orders. An empty owner matches nothing (fail-closed).
	List(ctx context.Context, owner string, params query.ListParams) ([]*Order, error)
	// UpdateStatus performs a conditional (compare-and-swap) status transition.
	// The update is applied only when the current persisted status equals
	// expectedStatus; if the actual status differs, UpdateStatus returns a
	// KindConflict error to prevent concurrent re-entry (e.g. two concurrent
	// Confirm calls both observing pending before either commits).
	UpdateStatus(ctx context.Context, id, expectedStatus, newStatus string) error
}
