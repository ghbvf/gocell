package projection

import "github.com/ghbvf/gocell/kernel/outbox"

// Cursor maps a consumed event to its monotonic stream position. outbox.Entry
// carries no sequence field — the position is supplied by the replay source.
//
// # Position invariants (required of every implementation)
//
//  1. Monotonic: within the same (cellID, projectionID) stream, Position is
//     non-decreasing across events delivered in order. DISTINCT events MUST get
//     STRICTLY INCREASING positions — if two distinct events shared a position,
//     the Coordinator's pos <= checkpoint guard would silently skip the second
//     after the first commits the checkpoint (a projection gap), so a coarser
//     cursor is unsound. The ONLY valid equality is RE-DELIVERY of an
//     already-applied event, which returns its same previously-assigned position;
//     that guard handles idempotent re-delivery. (RunCursorConformance asserts
//     strict increase across distinct seeded entries accordingly.)
//
//  2. 1-based: every valid event position is ≥ 1. The value 0 is reserved to mean
//     "no checkpoint / cold start" (the default returned by CheckpointStore on first
//     read). A Cursor must never return 0 for a real event.
//
//  3. Gap-allowed: the position sequence may have gaps (e.g. jumping from 3 to 7).
//     The Coordinator applies both ends of the gap as they arrive; events for the
//     missing positions 4–6 will be skipped if they arrive later because their
//     position is ≤ the checkpoint. This is expected and correct — gaps arise when
//     the event source does not emit every sequence number.
//
//  4. Resolution error semantics: an error returned by Position is treated as
//     transient by default (Coordinator requeues the event for retry). Wrap the
//     error in outbox.NewPermanentError to signal that the event is unrecoverable
//     and should be routed to the dead-letter exchange.
//
// PR-01 shipped this interface and the MemCursor test fake; the production
// outbox-journal-backed cursor (adapters/postgres.PGProjectionCursor, reading
// outbox_entries.seq) landed in PR-04c (#1368). Note the v1 limitation: positions
// come from the transient outbox relay, so the cursor/replay are gated to
// dev/preview until a durable projection journal lands (#1504).
//
// ref: Axon TrackingToken (position is a property of the token store / stream).
type Cursor interface {
	Position(entry outbox.Entry) (int64, error)
}
