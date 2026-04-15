// Package domain defines the core domain model for the order-cell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
)

// Order represents a todo-order aggregate.
type Order struct {
	ID        string
	Item      string
	Status    string // pending, confirmed
	CreatedAt time.Time
}

// OrderRepository abstracts order persistence.
type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	GetByID(ctx context.Context, id string) (*Order, error)
	List(ctx context.Context, params query.ListParams) ([]*Order, error)
}
