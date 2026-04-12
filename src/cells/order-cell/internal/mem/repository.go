// Package mem provides an in-memory implementation of the order domain repository.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
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
		return nil, errcode.New(errcode.ErrOrderNotFound,
			fmt.Sprintf("order %q not found", id))
	}
	// Return a copy.
	out := *o
	return &out, nil
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

	sortOrders(all, params.Sort)
	return applyOrderCursor(all, params), nil
}

// sortOrders sorts orders in-place by the given sort columns.
func sortOrders(orders []*domain.Order, cols []query.SortColumn) {
	if len(cols) == 0 {
		return
	}
	slices.SortFunc(orders, func(a, b *domain.Order) int {
		for _, col := range cols {
			v := compareOrderField(a, b, col.Name)
			if col.Direction == query.SortDESC {
				v = -v
			}
			if v != 0 {
				return v
			}
		}
		return 0
	})
}

// applyOrderCursor skips rows until past the cursor position, then limits.
func applyOrderCursor(orders []*domain.Order, params query.ListParams) []*domain.Order {
	start := 0
	if params.CursorValues != nil {
		for i, o := range orders {
			if orderAfterCursor(o, params.Sort, params.CursorValues) {
				start = i
				break
			}
			if i == len(orders)-1 {
				start = len(orders) // cursor past all rows
			}
		}
	}

	end := start + params.FetchLimit()
	if end > len(orders) {
		end = len(orders)
	}
	return orders[start:end]
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

// orderAfterCursor returns true if the order is strictly after the cursor
// position according to the sort columns and their directions.
func orderAfterCursor(o *domain.Order, cols []query.SortColumn, cursorValues []any) bool {
	for level := 0; level < len(cols); level++ {
		val := orderFieldValue(o, cols[level].Name)
		curVal := cursorValues[level]
		c := compareAny(val, curVal)

		if level < len(cols)-1 {
			if c != 0 {
				if cols[level].Direction == query.SortDESC {
					return c < 0
				}
				return c > 0
			}
			continue
		}
		// Last column: strict inequality.
		if cols[level].Direction == query.SortDESC {
			return c < 0
		}
		return c > 0
	}
	return false
}

func orderFieldValue(o *domain.Order, field string) any {
	switch field {
	case "created_at":
		return o.CreatedAt.Format(time.RFC3339Nano)
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

// compareAny compares two values that are either string or float64.
func compareAny(a, b any) int {
	aStr, aOk := a.(string)
	bStr, bOk := b.(string)
	if aOk && bOk {
		return cmp.Compare(aStr, bStr)
	}
	aFloat, aOk := a.(float64)
	bFloat, bOk := b.(float64)
	if aOk && bOk {
		return cmp.Compare(aFloat, bFloat)
	}
	panic(fmt.Sprintf("compareAny: unsupported type combination %T vs %T", a, b))
}
