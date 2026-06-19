package projection

import (
	"context"
	"time"
)

// DeadLetter is the durable record of a poison event the saga-journal Tailer
// could NOT project: business Apply returned a permanent error (bad payload,
// unknown event kind, malformed id — outbox.IsPermanent). It carries enough to
// locate the offending event in the durable journal (GlobalSeq + EventID +
// Stream) and a redacted reason, so an operator can triage without scanning the
// whole journal.
//
// PII safety: ErrorMessage MUST already be redacted by the caller
// (pkg/redaction.RedactError) before it reaches Record — the dead-letter table
// is operational state, not a place to leak a bad payload's contents.
type DeadLetter struct {
	CellID       string
	ProjectionID string
	// GlobalSeq is the poison event's monotonic journal position (the same value
	// the Tailer's checkpoint advances past when it skips the event).
	GlobalSeq int64
	EventID   string
	// Stream identifies the saga-journal stream (e.g. a saga definition ID) that
	// produced the poison event. Stream MUST NOT contain tenant information — this
	// is guaranteed by the projection declaration source (the stream name comes
	// from the saga definition, not from tenant-scoped data), so it is safe to
	// persist in the dead-letter table and include in structured log fields.
	Stream string
	// ErrorType is a coarse machine-filterable class (e.g. the errcode Code), or
	// empty when the permanent error is not an errcode. ErrorMessage is the
	// redacted human reason. ErrorType is extracted by errcodeOf via errors.As,
	// which unwraps through outbox.PermanentError.Unwrap(); therefore an
	// errcode.Error wrapped inside a PermanentError still yields its Code here.
	ErrorType    string
	ErrorMessage string
	// OccurredAt is the poison event's domain timestamp (event.OccurredAt()).
	OccurredAt time.Time
}

// DeadLetterStore is the durable sink for poison events the saga-journal Tailer
// skips. It is deliberately NARROW — Record only; the Tailer never reads back.
// Triage/replay tooling reads the table out-of-band (operator SQL today; a
// programmatic replay/evict surface is a separate concern, EPIC #1609).
//
// # Ambient-tx contract (PROJECTION-CHECKPOINT-TX-BOUND-01)
//
// Record participates in the caller's transaction via persistence.TxFromContext(ctx)
// — exactly like OwnerCheckpointStore.AdvanceIfOwner. It MUST NOT open its own
// transaction or connection. This is load-bearing: the Tailer records the
// dead-letter AND advances the checkpoint past the poison event in ONE
// transaction, so "skipped" and "recorded" commit atomically (a crash can never
// leave the checkpoint advanced past an unrecorded poison event). Implementations
// are held to the ambient-tx contract by PROJECTION-CHECKPOINT-TX-BOUND-01.
//
// Record MUST be idempotent on (CellID, ProjectionID, GlobalSeq): a re-driven
// skip (process crash before the advance committed, then replay) re-records the
// same poison event, which must not duplicate or error.
//
// ref: JasperFx/marten async-daemon DeadLetterEvent (mt_doc_deadletterevent) —
// skip-and-dead-letter on apply error, recorded in a queryable PG table.
// ref: AxonFramework SequencedDeadLetterQueue (dead_letter_entry) — durable
// dead-letter store backing a streaming event processor.
type DeadLetterStore interface {
	// Record durably stores one poison event's dead-letter entry inside the
	// ambient transaction. Idempotent on (CellID, ProjectionID, GlobalSeq).
	Record(ctx context.Context, dl DeadLetter) error
}
