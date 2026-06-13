// Package projection provides the order-status CQRS read model for the
// orderfulfillmentcell. The read model is built incrementally by
// HandleOrderEvent (the saga-journal projection Apply handler) and is queried
// synchronously by GetOrderStatus.
//
// This package is internal to the orderfulfillmentcell — it must not be
// imported by other cells or packages outside the orderfulfillmentcell tree.
package projection

import (
	"context"
	"sync"

	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
)

// OrderStatusReadModel is the read side of the order-status CQRS model.
// Upsert writes (or overwrites) the status for an order; Get reads it.
// Implementations must be safe for concurrent use from multiple goroutines.
type OrderStatusReadModel interface {
	// Upsert stores status for orderID, overwriting any existing value.
	Upsert(ctx context.Context, orderID string, status orderstatusgen.ResponseDataStatus) error
	// Get retrieves the current status for orderID. found is false when no
	// status has been upserted for the order yet (projection has not applied
	// any event for it).
	Get(ctx context.Context, orderID string) (status orderstatusgen.ResponseDataStatus, found bool, err error)
}

// MemReadModel is an in-memory implementation of OrderStatusReadModel backed
// by a plain map protected by a sync.RWMutex. Suitable for demo / unit-test
// mode only — it does not survive process restarts.
type MemReadModel struct {
	mu   sync.RWMutex
	rows map[string]orderstatusgen.ResponseDataStatus
}

// NewMemReadModel returns an empty MemReadModel ready for use.
func NewMemReadModel() *MemReadModel {
	return &MemReadModel{rows: make(map[string]orderstatusgen.ResponseDataStatus)}
}

// Upsert writes status for orderID. It always succeeds.
func (m *MemReadModel) Upsert(_ context.Context, orderID string, status orderstatusgen.ResponseDataStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[orderID] = status
	return nil
}

// Get reads the current status for orderID. found is false when no row exists.
func (m *MemReadModel) Get(_ context.Context, orderID string) (orderstatusgen.ResponseDataStatus, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.rows[orderID]
	return s, ok, nil
}

// compile-time interface check.
var _ OrderStatusReadModel = (*MemReadModel)(nil)
