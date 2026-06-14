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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// SagaEventEnvelope is the JSON shape a saga-journal ProjectionEvent.Payload()
// carries. It is the single source for the saga-journal projection payload
// contract: the carrier (toCarrier) marshals it and a consumer unmarshals into
// the SAME struct — a field rename breaks both sides at unmarshal, so the
// contract is enforced by the shared type, not by convention.
//
// Why an envelope: the journal records the event's discriminant in
// journal.Event.Kind, NOT in its opaque Payload. Terminal events written via
// MarkTerminal carry Payload: nil — their final state (succeeded / compensated /
// failed) lives ONLY in Kind. A projection consumer that received just the raw
// payload could not tell terminal outcomes apart (the gap #1391/PR-06 exposed).
// The envelope surfaces Kind (and StepName) alongside the original payload so a
// consumer can fold status, mirroring the journal's own deriveStatus fold (ADR
// #1609 §D6). Consumers recover the typed kind via journal.ParseEventKind(Kind).
type SagaEventEnvelope struct {
	// Kind is the journal.EventKind.String() label (snake_case, e.g.
	// "saga_succeeded"); reverse with journal.ParseEventKind. This is the same
	// label persisted in the PG saga_events.kind text column.
	Kind string `json:"kind"`
	// StepName is the originating step for step-scoped kinds; empty for
	// compensation_started and terminal kinds.
	StepName string `json:"stepName,omitempty"`
	// Payload is the original opaque step payload (journal.Event.Payload), or
	// null for events that carry none (e.g. terminal events). The journal
	// contract guarantees it is "opaque JSON object-or-null", so embedding it as
	// RawMessage keeps the envelope valid JSON.
	Payload json.RawMessage `json:"payload,omitempty"`
}

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

// SagaJournalEventIDPrefix is the prefix of every EventID produced by
// sagaProjectionEvent.EventID(). Consumers that parse the instanceID suffix
// (after the "@" separator) may match against this prefix to detect saga-journal
// events without hard-coding the full format string.
//
// Full format: "saga-journal:<globalSeq>@<instanceID>" — STABLE pre-v1.0.
const SagaJournalEventIDPrefix = "saga-journal:"

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
// Format: SagaJournalEventIDPrefix + "<globalSeq>@<instanceID>", i.e.
// "saga-journal:<globalSeq>@<instanceID>" — STABLE pre-v1.0. Consumers that
// parse the "@<instanceID>" suffix (e.g. to recover the saga instanceID) must
// use strings.LastIndex(eventID, "@") so that a future opaque instanceID
// containing "@" is still handled correctly.
func (e *sagaProjectionEvent) EventID() string {
	return fmt.Sprintf(SagaJournalEventIDPrefix+"%d@%s", e.globalSeq, e.instanceID)
}

// ParseSagaJournalEventID parses an EventID produced by sagaProjectionEvent.EventID()
// ("saga-journal:<globalSeq>@<instanceID>") into its parts, validating the prefix,
// a positive globalSeq, the '@' separator, and a non-empty instanceID. Returns a
// non-nil error (NOT wrapped permanent — caller decides) on any malformation.
//
// It splits on the LAST '@' (after stripping the prefix), mirroring the
// strings.LastIndex contract documented on EventID: the instanceID is the entire
// suffix after the final separator, so a future opaque instanceID that itself
// embeds '@' keeps its suffix intact. The portion before that last '@' is the
// globalSeq and MUST be a clean base-10 int64; a string that pushes a '@' into
// the seq portion is rejected as malformed (EventID never emits such a string,
// since globalSeq is always numeric). It is the round-trip inverse of EventID():
// EventID() output always parses back to the same (globalSeq, instanceID).
func ParseSagaJournalEventID(eventID string) (globalSeq int64, instanceID string, err error) {
	if !strings.HasPrefix(eventID, SagaJournalEventIDPrefix) {
		return 0, "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.ParseSagaJournalEventID: missing saga-journal EventID prefix")
	}
	rest := eventID[len(SagaJournalEventIDPrefix):]
	atIdx := strings.LastIndex(rest, "@")
	if atIdx < 0 {
		return 0, "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.ParseSagaJournalEventID: missing @ separator")
	}
	instanceID = rest[atIdx+1:]
	if instanceID == "" {
		return 0, "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.ParseSagaJournalEventID: empty instanceID suffix")
	}
	globalSeq, perr := strconv.ParseInt(rest[:atIdx], 10, 64)
	if perr != nil {
		return 0, "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.ParseSagaJournalEventID: globalSeq is not a base-10 int64")
	}
	if globalSeq <= 0 {
		return 0, "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sagaprojection.ParseSagaJournalEventID: globalSeq must be positive")
	}
	return globalSeq, instanceID, nil
}

// Payload returns the marshaled [SagaEventEnvelope] for this event: a JSON object
// {kind, stepName, payload} where kind is the journal.EventKind label, stepName
// the originating step (if any), and payload the original opaque step bytes (or
// null). It is NOT the raw step payload — the envelope wraps it so a consumer can
// recover the event Kind, which the terminal events (Payload: nil) carry only in
// Kind. Unmarshal into SagaEventEnvelope; recover the typed kind via
// journal.ParseEventKind.
//
// The returned slice is freshly marshaled per carrier (built by toCarrier) and is
// not aliased to any journal-internal storage, so it is safe to retain.
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
			e, err := toCarrier(ge)
			if err != nil {
				return err
			}
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
// projection.LiveCursor (the cursor contract projection.Coordinator requires).
//
// Why this method exists on a pull-only source: the saga read model is driven by the
// pull-only runtime/saga/tailer (which pages carriers via LoadSince and never receives a
// bare live broker push), so this resolver is never reached on the production path. It is
// required only because rebuild_wiring_test.go exercises SagaJournalSource through
// projection.NewCoordinator (to test rebuild identity handling), and the Coordinator's
// cursor slot is typed projection.LiveCursor. Resolution is therefore the identity on an
// intrinsic *sagaProjectionEvent carrier (what Replay/LoadSince produces); any other
// ProjectionEvent is a wiring error — the saga journal exposes no id→GlobalSeq live lookup —
// so it is permanent, never a recoverable transient.
func (s *SagaJournalSource) ResolveCarrier(_ context.Context, entry projection.ProjectionEvent) (projection.ProjectionEvent, error) {
	if _, ok := entry.(*sagaProjectionEvent); ok {
		return entry, nil
	}
	// Const message (no %T): a PermanentError reaches the DLX payload, so avoid leaking the
	// concrete carrier's internal package path.
	return nil, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"sagaprojection: ResolveCarrier resolves only intrinsic *sagaProjectionEvent carriers "+
			"(produced by Replay/LoadSince); the saga journal has no live bare-entry push path, so a "+
			"non-sagaProjectionEvent carrier is unresolvable (permanent)"))
}

// compile-time interface checks: SagaJournalSource is a ReplaySource and a full
// LiveCursor (Position + ResolveCarrier — the projection.Coordinator cursor contract).
var (
	_ projection.ReplaySource = (*SagaJournalSource)(nil)
	_ projection.LiveCursor   = (*SagaJournalSource)(nil)
)

// toCarrier converts a journal.GlobalEvent to the *sagaProjectionEvent carrier,
// marshaling a [SagaEventEnvelope] into the carrier payload so the consumer can
// recover the event Kind (which terminal events carry only in Kind, with a nil
// raw payload). The GlobalSeq is read from the outer GlobalEvent envelope (not
// the inner Event.GlobalSeq, which carries the same value but the outer field is
// the authoritative cross-instance sequence from the store layer).
//
// A marshal failure is wrapped as an outbox.PermanentError: the journal contract
// guarantees Event.Payload is "opaque JSON object-or-null", so this is
// unreachable in practice, but a malformed payload is a producer-side defect that
// must fail-closed (route to DLX), never silently drop the event's kind.
func toCarrier(ge journal.GlobalEvent) (*sagaProjectionEvent, error) {
	raw, err := json.Marshal(SagaEventEnvelope{
		Kind:     ge.Event.Kind.String(),
		StepName: ge.Event.StepName.String(),
		Payload:  json.RawMessage(ge.Event.Payload),
	})
	if err != nil {
		return nil, outbox.NewPermanentError(fmt.Errorf(
			"sagaprojection: marshal envelope for globalSeq=%d kind=%s: %w",
			ge.GlobalSeq, ge.Event.Kind, err))
	}
	return &sagaProjectionEvent{
		globalSeq:  ge.GlobalSeq,
		instanceID: ge.InstanceID.String(),
		payload:    raw,
		occurredAt: ge.Event.CreatedAt,
	}, nil
}
