package projection

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// JournalEvent is the durable projection-journal carrier: it wraps a reconstructed
// outbox.Entry together with the intrinsic global_seq the journal assigned at append
// time. Both the in-process MemProjectionEventSource and the PG PGProjectionEventSource
// deliver this single carrier type so that Cursor.Position reads the position from the
// carrier itself — never via a SELECT seq WHERE id=$1 lookup against a row the relay may
// have deleted. That deleted-row lookup is the #1504 root bug (cleaned outbox_entries →
// ErrNoRows → permanent error → dead-letter); reading the carrier's own global_seq closes
// it structurally.
//
// The ProjectionEvent methods delegate to the embedded Entry, so the carrier inherits the
// outbox no-overwrite RestoreContext (the producer's principal/observability envelope is
// restored, reusing the #1627 identity fix) and the Entry's EventID/Payload/OccurredAt/Stream.
//
// JournalEvent is exported (not a sealed funnel) for the same reason as the
// cellvocab.ProjectionEvent interface it satisfies: it is a read-only carrier. Forge
// protection for #1504 lives on the WRITE side — only the sanctioned same-transaction
// journaling writer may append rows the source reads (ADR §6 I2, PR-02) — not on this
// read-time wrapper.
//
// ref: AxonFramework TrackedEventMessage — the position (TrackingToken) rides with the
// event message rather than being re-queried from the store.
// ref: kernel/saga/sagaprojection.sagaProjectionEvent — the same carry-the-position shape.
type JournalEvent struct {
	entry     outbox.Entry
	globalSeq int64
}

// NewJournalEvent wraps a reconstructed Entry with the journal position the storage layer
// read alongside it (mem: 1-based dense index; PG: projection_events.global_seq). globalSeq
// must be >= 1 for a real journal row; 0 is the unseeded sentinel a conforming source never
// emits, which PositionFromCarrier rejects as a permanent error.
func NewJournalEvent(entry outbox.Entry, globalSeq int64) *JournalEvent {
	return &JournalEvent{entry: entry, globalSeq: globalSeq}
}

// EventID returns the wrapped Entry's globally-unique id (the projection_events.id / cursor key).
func (e *JournalEvent) EventID() string { return e.entry.EventID() }

// Payload returns the wrapped Entry's raw event body JSON.
func (e *JournalEvent) Payload() []byte { return e.entry.Payload() }

// OccurredAt returns the wrapped Entry's domain timestamp.
func (e *JournalEvent) OccurredAt() time.Time { return e.entry.OccurredAt() }

// Stream returns the wrapped Entry's routing topic.
func (e *JournalEvent) Stream() string { return e.entry.Stream() }

// RestoreContext delegates to the wrapped Entry (outbox no-overwrite principal +
// observability restore); it is idempotent and does not strip an ambient transaction.
func (e *JournalEvent) RestoreContext(ctx context.Context) context.Context {
	return e.entry.RestoreContext(ctx)
}

// GlobalSeq returns the intrinsic journal position carried by this event. It is the
// value PositionFromCarrier returns — the source never re-queries the store for it.
func (e *JournalEvent) GlobalSeq() int64 { return e.globalSeq }

// compile-time interface check.
var _ ProjectionEvent = (*JournalEvent)(nil)

// PositionFromCarrier resolves a ProjectionEvent's stream position by reading the
// intrinsic global_seq off the JournalEvent carrier — issuing NO database round-trip.
// This is the shared Cursor.Position implementation for both durable journal sources
// (mem + PG) and the structural #1504 fix: the position can never depend on a row the
// relay may have deleted.
//
// A non-JournalEvent carrier or a non-positive global_seq is unresolvable and cannot be
// fixed by retry, so it returns an outbox.PermanentError (Cursor invariant #4, cursor.go).
func PositionFromCarrier(entry ProjectionEvent) (int64, error) {
	e, ok := entry.(*JournalEvent)
	if !ok {
		return 0, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection: Position resolves only *JournalEvent carriers; an unresolvable carrier is permanent"))
	}
	if e.globalSeq <= 0 {
		return 0, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection: JournalEvent carries a non-positive global_seq (unseeded sentinel); a conforming journal source never emits seq 0"))
	}
	return e.globalSeq, nil
}
