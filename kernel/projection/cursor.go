package projection

import "github.com/ghbvf/gocell/kernel/outbox"

// Cursor maps a consumed event to its monotonic stream position. outbox.Entry
// carries no sequence field — the position is supplied by the replay source.
//
// # Position invariants (required of every implementation)
//
//  1. Monotonic (non-decreasing): within the same (cellID, projectionID) stream,
//     Position must be non-decreasing across events delivered in order. If an event
//     has already been applied, Position may return the same value again; the
//     Coordinator's pos <= checkpoint guard handles idempotent re-delivery.
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
// PR-01 ships this interface and a test fake only; the production
// journal/metadata-backed cursor implementation lands in PR-04 (#1176), where
// cellgen-derived wiring first needs a concrete Cursor to pass to Subscribe.
//
// ref: Axon TrackingToken (position is a property of the token store / stream).
type Cursor interface {
	Position(entry outbox.Entry) (int64, error)
}
