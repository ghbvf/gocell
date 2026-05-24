// Package domain defines the core domain model for the ordercell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Order status constants.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
)

// Order represents a todoorder aggregate.
type Order struct {
	ID        string
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
	List(ctx context.Context, params query.ListParams) ([]*Order, error)
	UpdateStatus(ctx context.Context, id, newStatus string) error
}
