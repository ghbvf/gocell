// Package mem provides an in-memory implementation of the order domain repository.
package mem

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ domain.OrderRepository = (*OrderRepository)(nil)

// OrderRepository is a thread-safe in-memory order store.
type OrderRepository struct {
	mu     sync.RWMutex
	orders map[string]*domain.Order
}

// NewOrderRepository creates an empty in-memory OrderRepository.
func NewOrderRepository() *OrderRepository {
	return &OrderRepository{orders: make(map[string]*domain.Order)}
}

// Create stores a new order. Returns an error if the ID already exists.
func (r *OrderRepository) Create(_ context.Context, order *domain.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.orders[order.ID]; exists {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("order %q already exists", order.ID))
	}
	// Store a copy to avoid external mutation.
	stored := *order
	r.orders[order.ID] = &stored
	return nil
}

// GetByID retrieves an order by ID.
func (r *OrderRepository) GetByID(_ context.Context, id string) (*domain.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	o, ok := r.orders[id]
	if !ok {
		return nil, errcode.New(errcode.ErrCellNotFound,
			fmt.Sprintf("order %q not found", id))
	}
	// Return a copy.
	out := *o
	return &out, nil
}

// List returns all orders in insertion-indeterminate order.
func (r *OrderRepository) List(_ context.Context) ([]*domain.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*domain.Order, 0, len(r.orders))
	for _, o := range r.orders {
		cp := *o
		result = append(result, &cp)
	}
	return result, nil
}
