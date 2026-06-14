package projection

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// MemProjectionEventSource is an in-process, append-only durable-journal source backed by
// a plain slice, the in-memory equivalent of the PG projection_events journal. It is the
// kernel-layer ReplaySource + Cursor used by fast unit tests and demos; it provides no
// database durability.
//
// Unlike MemReplaySource (which resolves Position by scanning entries for a matching
// EventID), this source wraps each appended entry in a *JournalEvent carrying its own
// 1-based global_seq, and Position reads that carrier field directly — never scanning the
// backing slice. That is the in-memory mirror of the #1504 structural fix: the position is
// intrinsic to the delivered event, not a lookup that a deletion could break.
//
// The SAME instance must be wired as both the Coordinator's ReplaySource and its Cursor so
// the global_seq encoding is consistent across the two interfaces (mirrors SagaJournalSource).
//
// Thread-safe: all methods are guarded by an RWMutex.
type MemProjectionEventSource struct {
	mu     sync.RWMutex
	events []*JournalEvent
}

// NewMemProjectionEventSource returns a ready-to-use empty source.
func NewMemProjectionEventSource() *MemProjectionEventSource {
	return &MemProjectionEventSource{}
}

// Append wraps entry with the next 1-based global_seq, stores it, and returns the carrier.
// This is the test/demo seed surface — a production source is read-only (the sanctioned
// same-transaction writer is the only append path; ADR §6 I2). Returning the carrier lets
// conformance seeds hand the exact delivered carrier to Cursor.Position.
func (m *MemProjectionEventSource) Append(entry outbox.Entry) *JournalEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := int64(len(m.events) + 1) // 1-based dense index
	carrier := NewJournalEvent(entry, seq)
	m.events = append(m.events, carrier)
	return carrier
}

// Replay iterates carriers with global_seq strictly greater than fromOffset in ascending
// order, calling fn for each. fromOffset==0 replays all. ctx cancellation is honored
// between events; fn's error stops replay immediately (delivered events are not retried).
func (m *MemProjectionEventSource) Replay(ctx context.Context, fromOffset int64, fn func(ProjectionEvent) error) error {
	m.mu.RLock()
	snapshot := make([]*JournalEvent, len(m.events))
	copy(snapshot, m.events)
	m.mu.RUnlock()

	for _, carrier := range snapshot {
		if carrier.globalSeq <= fromOffset {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(carrier); err != nil {
			return err
		}
	}
	return nil
}

// Head returns the highest assigned global_seq (= number of appended events), or 0 if empty.
func (m *MemProjectionEventSource) Head(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.events)), nil
}

// Position returns the carrier's intrinsic global_seq with no slice scan, delegating to the
// shared PositionFromCarrier. A non-JournalEvent carrier or the seq-0 sentinel is permanent.
func (m *MemProjectionEventSource) Position(entry ProjectionEvent) (int64, error) {
	return PositionFromCarrier(entry)
}

// ResolveCarrier normalizes a live-delivered entry into the position-bearing carrier this
// source already stored (LiveCarrierResolver). An already-positioned *JournalEvent is returned
// unchanged (idempotent — rebuild carriers re-resolve to themselves). A bare entry is matched by
// EventID against the appended events; the in-memory mirror of the PG SELECT-by-id lookup. An
// entry absent from the journal is a permanent error (Cursor invariant #4) — a bare entry never
// journaled cannot be assigned a position and retry cannot fix it.
func (m *MemProjectionEventSource) ResolveCarrier(_ context.Context, entry ProjectionEvent) (ProjectionEvent, error) {
	if _, ok := entry.(*JournalEvent); ok {
		return entry, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, carrier := range m.events {
		if carrier.EventID() == entry.EventID() {
			return carrier, nil
		}
	}
	return nil, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"projection: live entry not present in the in-memory projection journal; cannot resolve its global_seq"))
}

// compile-time interface checks: one type is a ReplaySource AND a full LiveCursor
// (Position + ResolveCarrier).
var (
	_ ReplaySource = (*MemProjectionEventSource)(nil)
	_ LiveCursor   = (*MemProjectionEventSource)(nil)
)
