// Package sagaprojection bridges kernel/saga/journal.GlobalReader to the
// projection harness (projection.ReplaySource + projection.Cursor +
// cellvocab.ProjectionEvent).
//
// PR-03 of EPIC #1609 (#1627). Design decisions at:
// docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md §D2.
//
// # Carrier identity
//
// Every saga journal event is delivered under the "system" principal sentinel
// (projection.SystemPrincipalActor) via sagaProjectionEvent.RestoreContext.
// Saga journal events carry no per-event author identity; the projection Apply
// function must never inherit an ambient admin identity from the Rebuild trigger
// context (see ADR #1609 §5, F1 flip). InstallSystemPrincipal performs an
// unconditional overwrite — contrast with outbox's no-overwrite RestoreToContext.
//
// # Position scheme
//
// Positions are the GlobalSeq values assigned by the journal backend (MemJournal:
// in-process monotonic counter; PG: BIGINT IDENTITY column, PR-PG). GlobalSeq is
// 1-based and stable across process restarts. The "unseeded" sentinel is globalSeq
// == 0 (the zero value; a conforming GlobalReader never emits seq 0).
// SagaJournalSource.Position returns a permanent error for the sentinel so that
// RunCursorConformance's "PermanentErrorOnUnknownEntry" sub-test passes.
//
// # Principal allowlist (PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01)
//
// This file is the single sanctioned caller of projection.InstallSystemPrincipal.
// The archtest in tools/archtest/projection_system_principal_install_caller_test.go
// locks this down with an anti-vacuity guard. Any other file calling
// InstallSystemPrincipal will fail CI immediately.
package sagaprojection

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// batchSize is the number of events fetched per LoadSince call in Replay.
// 256 is the same budget used by the outbox relay page size — large enough to
// amortize round-trip latency without allocating unbounded slices.
const batchSize = 256

// SagaJournalStream is the stream name reported by sagaProjectionEvent.Stream().
// The value is stable across restarts so it can be used as a projection filter
// or checkpoint namespace key.
//
// pre-v1.0 GA: this stream name may evolve directly; ".v1" is a naming
// convention, not a wire-contract lock. Post-GA evolution requires a version bump.
const SagaJournalStream = "saga.journal.v1"

// sagaProjectionEvent is the unexported carrier that adapts one journal.GlobalEvent
// to the cellvocab.ProjectionEvent interface.
//
// globalSeq == 0 is the "unseeded" sentinel (see package doc). A journal backend
// conforming to the GlobalReader spec never emits seq 0, so this sentinel is safe.
type sagaProjectionEvent struct {
	globalSeq  int64
	instanceID string // stringified idutil.SafeID for EventID uniqueness
	payload    []byte
	occurredAt time.Time
}

// EventID returns a stable, unique string identity for the event. It encodes both
// the cross-instance position (GlobalSeq) and the owning instance so that two
// events with the same GlobalSeq on different journals (impossible in practice but
// asserted by the conformance suite) are distinct.
//
// Format: "saga-journal:<globalSeq>@<instanceID>".
func (e *sagaProjectionEvent) EventID() string {
	return fmt.Sprintf("saga-journal:%d@%s", e.globalSeq, e.instanceID)
}

// Payload returns the raw opaque bytes appended by the saga step. May be empty or
// nil for events that carry no step output.
//
// The returned slice MUST NOT be mutated by the caller. MemJournal.LoadSince
// already deep-copies payload on read, so the carrier holds an independent
// copy with no alias to the journal's internal storage.
func (e *sagaProjectionEvent) Payload() []byte {
	return e.payload
}

// OccurredAt returns the wall-clock time at which the journal backend stamped the
// event on Append. This is the journal's CreatedAt (stamped by the injected clock),
// analogous to outbox.Entry.CreatedAt.
func (e *sagaProjectionEvent) OccurredAt() time.Time {
	return e.occurredAt
}

// Stream returns SagaJournalStream, the stable stream name for all saga journal
// events. This is analogous to outbox.Entry.RoutingTopic for the outbox path.
func (e *sagaProjectionEvent) Stream() string {
	return SagaJournalStream
}

// RestoreContext installs the "system" principal sentinel unconditionally in ctx,
// overwriting any ambient principal (including an admin's identity forwarded
// through context.WithoutCancel at the Rebuild detach boundary).
//
// This is the OVERWRITE variant — contrast with outbox.PrincipalMetadata.RestoreToContext
// (no-overwrite). The overwrite is necessary because saga journal events carry no
// per-event author identity; the projection Apply function must run under a known
// system identity, never an ambient admin's (ADR #1609 §5, F1 flip).
//
// Sanctioned caller: this file only (PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01).
func (e *sagaProjectionEvent) RestoreContext(ctx context.Context) context.Context {
	return projection.InstallSystemPrincipal(ctx)
}

// compile-time interface check.
var _ cellvocab.ProjectionEvent = (*sagaProjectionEvent)(nil)

// SagaJournalSource implements both projection.ReplaySource and projection.Cursor
// for the saga journal, backed by a journal.GlobalReader.
//
// # Position scheme
//
// The cursor position of each event IS its GlobalSeq. This is intrinsic: a
// GlobalReader guarantees globally monotonic, 1-based GlobalSeq values, satisfying
// the Cursor invariants (1-based, monotonic, gap-allowed) directly.
//
// # Thread safety
//
// SagaJournalSource holds no mutable state; all methods are safe for concurrent use
// from multiple goroutines. The underlying GlobalReader is assumed to be safe for
// concurrent use (MemJournal satisfies this with a sync.RWMutex).
type SagaJournalSource struct {
	gr journal.GlobalReader
}

// NewSagaJournalSource returns a SagaJournalSource backed by the given GlobalReader,
// or a non-nil error if gr is nil.
//
// The returned source implements both projection.ReplaySource and projection.Cursor;
// the SAME instance must be wired into both CoordinatorConfig.Replay and
// CoordinatorConfig.Cursor to ensure the Position encoding (GlobalSeq) is consistent
// across the two interfaces.
//
// ref: kernel/projection.NewCoordinator (error-return pattern for required deps)
func NewSagaJournalSource(gr journal.GlobalReader) (*SagaJournalSource, error) {
	if validation.IsNilInterface(gr) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.NewSagaJournalSource: GlobalReader required")
	}
	return &SagaJournalSource{gr: gr}, nil
}

// Head returns the highest GlobalSeq in the journal, or 0 if the journal is empty.
// This satisfies projection.ReplaySource.Head.
func (s *SagaJournalSource) Head(ctx context.Context) (int64, error) {
	return s.gr.HeadSeq(ctx)
}

// Replay iterates journal events with GlobalSeq strictly greater than fromOffset,
// calling fn for each in ascending GlobalSeq order. fromOffset == 0 replays all
// events. If fn returns a non-nil error, Replay stops immediately and returns it.
//
// Internally, Replay paginates via LoadSince with batchSize == 256 to bound
// per-call allocations. The loop stops when a batch returns fewer than batchSize
// events (caught up or empty journal).
//
// This satisfies projection.ReplaySource.Replay.
func (s *SagaJournalSource) Replay(ctx context.Context, fromOffset int64, fn func(projection.ProjectionEvent) error) error {
	cursor := fromOffset
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := s.gr.LoadSince(ctx, cursor, batchSize)
		if err != nil {
			return fmt.Errorf("sagaprojection: LoadSince(after=%d, limit=%d): %w", cursor, batchSize, err)
		}
		for _, ge := range batch {
			e := toCarrier(ge)
			if err := fn(e); err != nil {
				return err
			}
			// Advance cursor to the last delivered GlobalSeq so the next
			// LoadSince page starts after it.
			cursor = ge.GlobalSeq
		}
		// Caught-up or empty: stop paging.
		if len(batch) < batchSize {
			return nil
		}
	}
}

// Position returns the GlobalSeq of entry as its stable cursor position.
// If entry is not a *sagaProjectionEvent (wrong carrier type) or is the unseeded
// sentinel (GlobalSeq == 0), Position returns a permanent error per Cursor
// invariant #4 (cursor.go): an unresolvable entry is unrecoverable.
//
// This satisfies projection.Cursor.Position.
func (s *SagaJournalSource) Position(entry projection.ProjectionEvent) (int64, error) {
	e, ok := entry.(*sagaProjectionEvent)
	if !ok {
		return 0, outbox.NewPermanentError(fmt.Errorf(
			"sagaprojection: Position received unknown carrier type %T; "+
				"SagaJournalSource.Cursor only resolves *sagaProjectionEvent carriers "+
				"(Cursor invariant #4: unresolvable entry is permanent)",
			entry,
		))
	}
	if e.globalSeq <= 0 {
		return 0, outbox.NewPermanentError(fmt.Errorf(
			"sagaprojection: Position received unseeded sentinel (globalSeq=%d); "+
				"saga journal events always have GlobalSeq >= 1 "+
				"(Cursor invariant #4: unresolvable entry is permanent)",
			e.globalSeq,
		))
	}
	return e.globalSeq, nil
}

// ResolveCarrier satisfies projection.LiveCarrierResolver so SagaJournalSource is a
// projection.LiveCursor (the cursor contract projection.Coordinator requires). The saga
// read model is driven by the pull-only runtime/saga/tailer (which pages carriers via
// LoadSince) and, when exercised through a Coordinator, only ever replays journal-produced
// *sagaProjectionEvent carriers — there is NO live broker push of a bare entry. So
// resolution is the identity on an intrinsic carrier; any other ProjectionEvent is
// unresolvable and permanent (the saga journal exposes no id→GlobalSeq live lookup, and a
// bare entry on this source would be a wiring error, not a recoverable transient).
func (s *SagaJournalSource) ResolveCarrier(_ context.Context, entry projection.ProjectionEvent) (projection.ProjectionEvent, error) {
	if _, ok := entry.(*sagaProjectionEvent); ok {
		return entry, nil
	}
	return nil, outbox.NewPermanentError(fmt.Errorf(
		"sagaprojection: ResolveCarrier resolves only *sagaProjectionEvent carriers produced by Replay/LoadSince; "+
			"the saga journal has no live bare-entry push path (the read model is driven by the pull-only tailer), "+
			"so a %T is unresolvable (permanent)", entry))
}

// compile-time interface checks: SagaJournalSource is a ReplaySource and a full
// LiveCursor (Position + ResolveCarrier — the projection.Coordinator cursor contract).
var (
	_ projection.ReplaySource = (*SagaJournalSource)(nil)
	_ projection.LiveCursor   = (*SagaJournalSource)(nil)
)

// toCarrier converts a journal.GlobalEvent to the *sagaProjectionEvent carrier.
// The GlobalSeq is read from the outer GlobalEvent envelope (not the inner
// Event.GlobalSeq, which carries the same value but the outer field is the
// authoritative cross-instance sequence from the store layer).
func toCarrier(ge journal.GlobalEvent) *sagaProjectionEvent {
	return &sagaProjectionEvent{
		globalSeq:  ge.GlobalSeq,
		instanceID: ge.InstanceID.String(),
		payload:    ge.Event.Payload,
		occurredAt: ge.Event.CreatedAt,
	}
}
