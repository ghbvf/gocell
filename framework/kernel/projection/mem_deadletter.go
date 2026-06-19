package projection

import (
	"context"
	"sync"
)

// MemDeadLetterStore is an in-process DeadLetterStore for tests and demos. It
// does NOT participate in any database transaction (ctx is accepted but unused —
// the ambient-tx binding is the caller's concern, the same exemption
// MemOwnerCheckpointStore takes). Durability is process-lifetime only.
//
// Record is idempotent on (CellID, ProjectionID, GlobalSeq): a re-driven skip
// (the Tailer crashed before the advance committed, then replayed) re-records
// the same poison event, which is a no-op here, mirroring the PG impl's
// ON CONFLICT DO NOTHING. Thread-safe via a mutex.
type MemDeadLetterStore struct {
	mu      sync.Mutex
	seen    map[deadLetterKey]struct{}
	records []DeadLetter
}

// deadLetterKey is the idempotency key: one poison event per (cell, projection,
// journal position).
type deadLetterKey struct {
	cellID       string
	projectionID string
	globalSeq    int64
}

// NewMemDeadLetterStore returns a ready-to-use MemDeadLetterStore.
func NewMemDeadLetterStore() *MemDeadLetterStore {
	return &MemDeadLetterStore{seen: make(map[deadLetterKey]struct{})}
}

// Record appends the dead-letter entry, deduplicating on
// (CellID, ProjectionID, GlobalSeq). ctx is accepted but unused — not tx-bound.
func (m *MemDeadLetterStore) Record(_ context.Context, dl DeadLetter) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := deadLetterKey{cellID: dl.CellID, projectionID: dl.ProjectionID, globalSeq: dl.GlobalSeq}
	if _, ok := m.seen[k]; ok {
		return nil
	}
	m.seen[k] = struct{}{}
	m.records = append(m.records, dl)
	return nil
}

// Records returns a copy of all recorded dead letters, for test assertions. Not
// part of the DeadLetterStore interface — the Tailer never reads back.
func (m *MemDeadLetterStore) Records() []DeadLetter {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeadLetter, len(m.records))
	copy(out, m.records)
	return out
}

// compile-time interface check.
var _ DeadLetterStore = (*MemDeadLetterStore)(nil)
