package projection

import (
	"context"
	"sync"
)

// MemCheckpointStore is an in-process CheckpointStore backed by a plain map.
// It is suitable for tests and demos; it does NOT participate in any database
// transaction (ctx is accepted but unused — ambient-tx binding is the caller's
// concern). Durability is process-lifetime only.
//
// Thread-safe: concurrent SaveOffset / LoadOffset calls are protected by
// a RWMutex.
type MemCheckpointStore struct {
	mu      sync.RWMutex
	offsets map[string]int64
}

// NewMemCheckpointStore returns a ready-to-use MemCheckpointStore.
func NewMemCheckpointStore() *MemCheckpointStore {
	return &MemCheckpointStore{offsets: make(map[string]int64)}
}

// checkpointKey is the map key for (cellID, projectionID). Named checkpointKey
// (not key) to avoid a redeclaration conflict with contract_test.go's package-
// level func key(cellID, projectionID string) string which is used by the
// PR-00 fake implementation.
func checkpointKey(cellID, projectionID string) string {
	return cellID + "/" + projectionID
}

// LoadOffset returns the committed offset for (cellID, projectionID), or 0 if
// no offset has been recorded yet (cold start). ctx is accepted but unused.
func (m *MemCheckpointStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.offsets[checkpointKey(cellID, projectionID)], nil
}

// SaveOffset persists offset for (cellID, projectionID). ctx is accepted but
// unused — MemCheckpointStore is not tx-bound.
func (m *MemCheckpointStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offsets[checkpointKey(cellID, projectionID)] = offset
	return nil
}

// compile-time interface check.
var _ CheckpointStore = (*MemCheckpointStore)(nil)
