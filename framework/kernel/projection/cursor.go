package projection

import "context"

// Cursor maps a consumed event to its monotonic stream position. The
// ProjectionEvent carrier exposes no sequence field — the position is supplied by
// the replay source.
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
// The production cursor is the durable journal source (adapters/postgres.
// PGProjectionEventSource / MemProjectionEventSource), which reads its position
// from the JournalEvent carrier's intrinsic global_seq (no store lookup). The
// in-memory MemCursor (paired with MemReplaySource) is a test/demo fake that
// resolves position by EventID scan. The saga read model reuses this same Cursor
// contract via kernel/saga/sagaprojection.SagaJournalSource (replay-only, driven
// by runtime/saga/tailer — no live push path).
//
// ref: Axon TrackingToken (position is a property of the token store / stream).
type Cursor interface {
	Position(entry ProjectionEvent) (int64, error)
}

// LiveCarrierResolver normalizes a live-delivered event into a position-bearing
// carrier so that Cursor.Position can resolve it.
//
// # Why this exists (the live-path gap)
//
// On the REBUILD path a ReplaySource produces position-bearing carriers itself
// (JournalEvent with an intrinsic global_seq), so Cursor.Position is closed. But
// on the LIVE path ConsumerBase pushes a bare outbox.Entry with NO global_seq;
// feeding that straight to a carrier-intrinsic Cursor.Position is a permanent
// error. ResolveCarrier closes that gap at the delivery boundary:
//
//   - carrier-intrinsic sources (Mem/PGProjectionEventSource) look the event's
//     journal position up by id and wrap it in a *JournalEvent. The lookup cannot
//     fail spuriously: the D4 emit-time same-transaction double-write commits the
//     projection_events row BEFORE the event is delivered, and the journal is
//     never cleaned (migration 058 REVOKE) — so a genuinely-absent row is a
//     permanent error (outbox.PermanentError), a query fault is transient.
//   - an already-positioned *JournalEvent is returned unchanged (idempotent), so
//     the rebuild carrier and a re-resolved live carrier are interchangeable.
//   - EventID-scan sources (MemCursor) return the bare entry unchanged — their
//     Position already accepts a bare entry.
//
// ref: AxonFramework TrackingToken — the position rides with the delivered event
// rather than being re-queried from the store at apply time. ADR 202606071600-1504 §4.2.
type LiveCarrierResolver interface {
	ResolveCarrier(ctx context.Context, entry ProjectionEvent) (ProjectionEvent, error)
}

// LiveCursor is the cursor contract required by every projection.Coordinator: it
// both reads a known carrier's position (Cursor) AND resolves a bare live entry
// to a position-bearing carrier (LiveCarrierResolver). The Coordinator's cursor
// slot is typed LiveCursor so a cursor that cannot resolve live carriers is a
// COMPILE error, not a runtime dead-letter — every projection has a live push
// path (buildHandler), so live resolution is mandatory, never optional.
//
// Keeping resolution off the base Cursor interface (a separate LiveCarrierResolver
// composed into LiveCursor) is what bounds the blast radius to the Coordinator's
// cursor slot: a plain Cursor consumer — runtime/saga/tailer (replay/pull-only, it
// pages the journal via LoadSince and never receives a bare live push), the conformance
// enroll fixtures, the tailer test fakes — needs no ResolveCarrier. Only a type wired
// as a Coordinator cursor must be a LiveCursor. SagaJournalSource is one such type (a
// kernel test drives it through projection.Coordinator), so it implements LiveCursor
// with a protocol-declaring ResolveCarrier (identity on its intrinsic carrier; permanent
// on a bare entry, since the saga journal has no live bare-entry push) — see
// kernel/saga/sagaprojection.SagaJournalSource.ResolveCarrier.
type LiveCursor interface {
	Cursor
	LiveCarrierResolver
}
