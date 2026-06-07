package projection

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// MemOwnerCheckpointStore is an in-process OwnerCheckpointStore for tests and
// demos. It also implements the base CheckpointStore (LoadOffset + SaveOffset)
// so a single value satisfies both the harness offset contract and the Tailer's
// fenced-advance contract. It does NOT participate in any database transaction
// (ctx is accepted but unused — ambient-tx binding is the caller's concern, the
// same exemption MemCheckpointStore takes). Durability is process-lifetime only.
//
// Thread-safe: concurrent LoadOffset / SaveOffset / AdvanceIfOwner calls are
// protected by an RWMutex.
//
// Fencing model (semantics B; single source = OwnerCheckpointStore godoc): an
// advance is accepted when the caller already owns the checkpoint (token ==
// recorded owner) OR the requested offset is strictly ahead of the committed
// offset (a legitimate new leader claiming past the high-water mark). Otherwise
// it is rejected with ErrStaleOwner. SaveOffset (the base contract) is
// unconditional and leaves the owner untouched — only AdvanceIfOwner is fenced;
// the saga Tailer never calls SaveOffset.
type MemOwnerCheckpointStore struct {
	mu      sync.RWMutex
	entries map[string]ownerEntry
}

// ownerEntry is the per-(cellID, projectionID) committed offset plus the owner
// token that last advanced it.
type ownerEntry struct {
	owner  string
	offset int64
}

// NewMemOwnerCheckpointStore returns a ready-to-use MemOwnerCheckpointStore.
func NewMemOwnerCheckpointStore() *MemOwnerCheckpointStore {
	return &MemOwnerCheckpointStore{entries: make(map[string]ownerEntry)}
}

// LoadOffset returns the committed offset for (cellID, projectionID), or 0 if
// none has been recorded yet (cold start). ctx is accepted but unused.
func (m *MemOwnerCheckpointStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entries[checkpointKey(cellID, projectionID)].offset, nil
}

// SaveOffset persists offset unconditionally (base CheckpointStore contract),
// leaving the owner token unchanged. ctx is accepted but unused — not tx-bound.
func (m *MemOwnerCheckpointStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := checkpointKey(cellID, projectionID)
	e := m.entries[k]
	e.offset = offset
	m.entries[k] = e
	return nil
}

// AdvanceIfOwner advances the committed offset fenced by ownerToken (semantics B,
// see the type / interface doc). ctx is accepted but unused — not tx-bound.
// An empty ownerToken is rejected fail-closed (a real Tailer always mints a
// non-empty UUID per lock acquisition; an empty token would otherwise match a
// cold empty-owner row and claim it without distlock backing).
func (m *MemOwnerCheckpointStore) AdvanceIfOwner(_ context.Context, cellID, projectionID, ownerToken string, offset int64) error {
	if ownerToken == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection: AdvanceIfOwner requires a non-empty owner token")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := checkpointKey(cellID, projectionID)
	e := m.entries[k]
	if ownerToken == e.owner || offset > e.offset {
		m.entries[k] = ownerEntry{owner: ownerToken, offset: offset}
		return nil
	}
	return ErrStaleOwner
}

// compile-time interface checks.
var (
	_ OwnerCheckpointStore = (*MemOwnerCheckpointStore)(nil)
	_ CheckpointStore      = (*MemOwnerCheckpointStore)(nil)
)
