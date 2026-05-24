// Package mem provides an in-memory implementation of the order domain repository.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"order already exists",
			errcode.WithDetails(slog.String("orderId", order.ID)))
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
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound,
			"order not found",
			errcode.WithDetails(slog.String("orderId", id)))
	}
	// Return a copy.
	out := *o
	return &out, nil
}

// UpdateStatus performs a conditional status transition (compare-and-swap).
// The update is applied only when the current persisted status equals expectedStatus.
// Returns ErrOrderNotFound if no order with the given ID exists.
// Returns ErrConflict (KindConflict) if the current status does not match expectedStatus,
// indicating a concurrent modification won the race and this transition should be rejected.
func (r *OrderRepository) UpdateStatus(_ context.Context, id, expectedStatus, newStatus string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	o, ok := r.orders[id]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound,
			"order not found",
			errcode.WithDetails(slog.String("orderId", id)))
	}
	if o.Status != expectedStatus {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"order status precondition failed",
			errcode.WithDetails(
				slog.String("orderId", id),
				slog.String("expected", expectedStatus),
				slog.String("actual", o.Status),
			))
	}
	o.Status = newStatus
	return nil
}

// List returns orders sorted and paginated according to params.
// It applies keyset cursor filtering and returns up to FetchLimit() rows
// for N+1 hasMore detection.
func (r *OrderRepository) List(_ context.Context, params query.ListParams) ([]*domain.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*domain.Order, 0, len(r.orders))
	for _, o := range r.orders {
		cp := *o
		all = append(all, &cp)
	}

	query.Sort(all, params.Sort, compareOrderField)
	result, err := query.ApplyCursor(all, params, orderFieldValue)
	if err != nil {
		return nil, fmt.Errorf("order-repo: list: %w", err)
	}
	return result, nil
}

// compareOrderField compares a single field of two orders.
func compareOrderField(a, b *domain.Order, field string) int {
	switch field {
	case "created_at":
		return a.CreatedAt.Compare(b.CreatedAt)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	case "item":
		return cmp.Compare(a.Item, b.Item)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	default:
		return 0
	}
}

// orderFieldValue extracts a cursor-comparable value from an order.
func orderFieldValue(o *domain.Order, field string) any {
	switch field {
	case "created_at":
		return o.CreatedAt
	case "id":
		return o.ID
	case "item":
		return o.Item
	case "status":
		return o.Status
	default:
		return ""
	}
}
