package projection

import (
	"context"
	"sync"
)

// ReplaySource is the read-model event store interface consumed by a Coordinator
// rebuild. It delivers past events in deterministic ascending position order so
// the rebuild can apply them exactly once from offset 0 (or any resume point).
//
// # Position scheme
//
// Positions are 1-based monotonically increasing integers. Position 0 means
// "no events / cold start" (same sentinel as CheckpointStore.LoadOffset cold
// start). Each event returned by Replay has a position > fromOffset, and
// positions across multiple Replay calls on the same source are stable
// (deterministic, not ephemeral).
//
// # Transaction semantics
//
// ReplaySource.Replay is NOT transactional: it iterates and calls fn for each
// qualifying event. The Coordinator wraps each fn call in RunInTx so that the
// Apply and SaveOffset commit atomically. Replay does not open or join any
// ambient transaction — it is the caller's responsibility to manage tx
// boundaries around fn.
//
// # Replay duration bound (v1)
//
// In v1, Replay is bounded only by ctx cancellation: there is no max-entries
// or max-duration parameter. Operators bound replay duration by setting a
// timeout on the rebuild context (the ctx passed to Rebuild, which is
// forwarded as the runRebuild ctx). Per-batch limiting is deferred to a future
// version when large-history projections require it.
//
// The carrier is the typed [ProjectionEvent] interface (EPIC #1609 PR-01): the
// durable journal source (adapters/postgres.PGProjectionEventSource, EPIC #1504)
// and the saga-journal source both satisfy it; neither leaks the concrete outbox.Entry.
//
// ref: AxonFramework EventStore — ordered event stream replay by position/token.
// ref: JasperFx/marten IDocumentSession.Events.QueryAllRawEvents — append-only
// event store replay.
type ReplaySource interface {
	// Replay iterates events with position strictly greater than fromOffset in
	// ascending position order, calling fn for each. fromOffset==0 replays all
	// events. If fn returns an error, Replay stops immediately and returns that
	// error; events already passed to fn are NOT retried.
	Replay(ctx context.Context, fromOffset int64, fn func(ProjectionEvent) error) error

	// Head returns the highest available position in the source, or 0 if the
	// source is empty. Head is used to determine the rebuild cutoff and to
	// compute the pending_events metric (Head − checkpoint).
	Head(ctx context.Context) (int64, error)
}

// MemReplaySource is an in-process append-only ReplaySource backed by a plain
// slice. It is suitable for tests and demos only; it does NOT participate in
// any database transaction and provides no durability.
//
// # Position scheme
//
// Position is 1-based insertion index: the first Append call assigns position 1,
// the second assigns position 2, etc. This scheme is deterministic, gap-free,
// and agrees with the paired test memCursor.Position(e) implementation that uses
// positionOf(e) to look up the 1-based index.
//
// # Event identity
//
// Position resolution matches events by their EventID (the ProjectionEvent
// carrier's identity accessor), which the carrier contract requires to be unique
// across the whole replay source — so the lookup is collision-free even when test
// events are created in the same nanosecond and does not rely on any pointer or
// timestamp identity.
//
// Thread-safe: all methods are protected by a RWMutex.
type MemReplaySource struct {
	mu      sync.RWMutex
	entries []ProjectionEvent
}

// NewMemReplaySource returns a ready-to-use MemReplaySource.
func NewMemReplaySource() *MemReplaySource {
	return &MemReplaySource{}
}

// Append adds entry to the source at the next 1-based insertion position.
// This is a test helper — production code does not call Append directly.
func (m *MemReplaySource) Append(entry ProjectionEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
}

// Replay iterates entries with 1-based position > fromOffset in insertion order,
// calling fn for each. Returns fn's error immediately if non-nil. ctx.Done is
// checked at the start of each iteration to respect cancellation.
func (m *MemReplaySource) Replay(ctx context.Context, fromOffset int64, fn func(ProjectionEvent) error) error {
	m.mu.RLock()
	snapshot := make([]ProjectionEvent, len(m.entries))
	copy(snapshot, m.entries)
	m.mu.RUnlock()

	for i, e := range snapshot {
		pos := int64(i + 1) // 1-based insertion index
		if pos <= fromOffset {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// Head returns the number of entries (= highest available 1-based position), or 0
// if empty. ctx is accepted but unused (satisfies ReplaySource interface).
func (m *MemReplaySource) Head(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int64(len(m.entries)), nil
}

// positionOf returns the 1-based insertion index of entry in the source by its
// EventID, or 0 if not found. EventID is unique per event so this comparison is
// collision-free even when events are created in the same nanosecond.
//
// This is a test helper — production code should not call this method.
//
// Cursor/Replay position coupling: the paired memCursor.Position(e) calls this
// method so that Coordinator.applyOne receives the same 1-based position that
// Replay delivers entries with. The coupling ensures position agreement between
// the replay source and the cursor.
//
// Note: Position (public) is the stable cross-package alias used by projectiontest
// conformance helpers and test cursors; both are identical.
func (m *MemReplaySource) positionOf(e ProjectionEvent) int64 {
	return m.Position(e)
}

// Position returns the 1-based insertion index of entry in the source by its
// EventID, or 0 if not found. Used by the projectiontest conformance helper and
// test cursors which need a stable cross-package API. EventID is unique per event
// so this lookup is collision-free even for same-nanosecond events.
func (m *MemReplaySource) Position(e ProjectionEvent) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i, stored := range m.entries {
		if stored.EventID() == e.EventID() {
			return int64(i + 1)
		}
	}
	return 0
}

// compile-time interface check.
var _ ReplaySource = (*MemReplaySource)(nil)
