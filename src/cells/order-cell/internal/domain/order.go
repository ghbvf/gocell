// Package domain defines the core domain model for the order-cell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
)

// Order represents a todo-order aggregate.
type Order struct {
	ID        string    `json:"id"`
	Item      string    `json:"item"`
	Status    string    `json:"status"` // pending, confirmed
	CreatedAt time.Time `json:"createdAt"`
}

// OrderRepository abstracts order persistence.
type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	GetByID(ctx context.Context, id string) (*Order, error)
	List(ctx context.Context, params query.ListParams) ([]*Order, error)
}
