package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// EventKind classifies a durable saga journal event. The vocabulary is fully
// state-transition-bearing: every lifecycle move a saga makes has a
// corresponding event kind, so the append-only log alone replays the complete
// history — including WHICH terminal state was reached and WHEN the rollback
// phase began. The instance Status projection is a deterministic fold of these
// kinds, never a separate source of truth.
//
// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid kind, so a
// forgotten/uninitialised Event.Kind never silently appears valid — the same
// discipline as saga.Status.
type EventKind uint8

const (
	// Step-level events — appended via Append under the holder's lease; each
	// carries a StepName.
	KindStepStarted     EventKind = iota + 1 // step_started
	KindStepCompleted                        // step_completed
	KindStepFailed                           // step_failed
	KindStepCompensated                      // step_compensated — a compensation step result; legal only while Compensating

	// KindCompensationStarted marks the saga entering the rollback phase
	// (Running → Compensating). Appended via Append; saga-scoped (no StepName).
	// The Coordinator appends it when it DECIDES to compensate, before any
	// compensation runs — so a leader handoff mid-rollback reads Compensating,
	// not Running.
	KindCompensationStarted // compensation_started

	// Terminal events — appended ONLY via MarkTerminal, atomically with the
	// projection's terminal status flip. The terminal status is encoded in the
	// kind itself, so the log alone replays the final state.
	KindSagaSucceeded   // saga_succeeded
	KindSagaFailed      // saga_failed
	KindSagaCompensated // saga_compensated
	KindSagaExpired     // saga_expired

	// KindStepCompensationFailed — a compensation step's CompensateFunc returned
	// a non-nil error. Legal only while Compensating; status unchanged. Issued
	// per-step (carries StepName) and distinct from KindStepFailed (which is a
	// forward Step.Run failure during Running phase). Appended at iota position
	// 10 (after the terminal kinds) so existing kind wire values 1..9 stay
	// stable — adding a new kind in the middle would re-number persisted rows.
	// Introduced by #1181 to give reverse-compensation failures a wire-distinct
	// vocabulary (previously runCompensation co-opted KindStepFailed, which
	// the Compensating-phase projection rejected).
	KindStepCompensationFailed // step_compensation_failed

	// KindSagaCompensationFailed is the terminal journal event for
	// saga.StatusCompensationFailed. Written exclusively via MarkTerminal when
	// the compensation phase completed but at least one CompensateFunc returned a
	// non-nil error. Wire value 11 (appended after KindStepCompensationFailed at
	// position 10) to keep existing kind values 1..10 stable.
	// Introduced by #1210 to give the "rollback failed" terminal state a
	// dedicated event kind distinct from KindSagaFailed ("forward failed").
	KindSagaCompensationFailed // saga_compensation_failed
)

// Valid reports whether k is a recognized EventKind.
func (k EventKind) Valid() bool {
	return k >= KindStepStarted && k <= KindSagaCompensationFailed
}

// String returns a human-readable label for k. The snake_case labels map 1:1
// to the persisted saga_events.kind text column (PR-04).
func (k EventKind) String() string {
	switch k {
	case KindStepStarted:
		return "step_started"
	case KindStepCompleted:
		return "step_completed"
	case KindStepFailed:
		return "step_failed"
	case KindStepCompensated:
		return "step_compensated"
	case KindCompensationStarted:
		return "compensation_started"
	case KindSagaSucceeded:
		return "saga_succeeded"
	case KindSagaFailed:
		return "saga_failed"
	case KindSagaCompensated:
		return "saga_compensated"
	case KindSagaExpired:
		return "saga_expired"
	case KindStepCompensationFailed:
		return "step_compensation_failed"
	case KindSagaCompensationFailed:
		return "saga_compensation_failed"
	default:
		return fmt.Sprintf("eventkind(%d)", k)
	}
}

// isStepKind reports whether k is a per-step event (requires a StepName).
// KindCompensationStarted and the terminal kinds are saga-scoped, not
// step-scoped.
func (k EventKind) isStepKind() bool {
	switch k {
	case KindStepStarted, KindStepCompleted, KindStepFailed, KindStepCompensated,
		KindStepCompensationFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether k is a terminal saga event (written only by
// MarkTerminal). Exported so the PR-04 PG store and the conformance suite share
// one definition.
func (k EventKind) IsTerminal() bool {
	switch k {
	case KindSagaSucceeded, KindSagaFailed, KindSagaCompensated, KindSagaExpired,
		KindSagaCompensationFailed:
		return true
	default:
		return false
	}
}

// TerminalEventKind maps a terminal saga.Status to the event kind MarkTerminal
// records for it, so the log encodes the final state. ok is false for a
// non-terminal status.
func TerminalEventKind(s saga.Status) (EventKind, bool) {
	switch s {
	case saga.StatusSucceeded:
		return KindSagaSucceeded, true
	case saga.StatusFailed:
		return KindSagaFailed, true
	case saga.StatusCompensated:
		return KindSagaCompensated, true
	case saga.StatusExpired:
		return KindSagaExpired, true
	case saga.StatusCompensationFailed:
		return KindSagaCompensationFailed, true
	default:
		return 0, false
	}
}

// Event is one durable, append-only record in a saga instance's history. The
// owning instance is identified by the Append(instanceID, ...) argument, so it
// is deliberately not duplicated as a field here. Version is assigned by
// Journal.Append (monotonic per instance, starting at 1) and CreatedAt is
// stamped by the Journal from its injected clock; callers leave both zero on
// the way in.
type Event struct {
	Version int64 // per-instance position, assigned by Append; zero on input
	// GlobalSeq is the cross-instance global position — monotonic over the whole
	// journal, NOT per-instance like Version — assigned by the Journal on append
	// (in-memory from a global counter; PG from a BIGINT IDENTITY column, PR-PG).
	// It is the stable total order [GlobalReader] scans by and the value a
	// projection cursor checkpoints on (see [GlobalEvent]). Additive (#1609
	// PR-02): callers leave it zero on input, and a backend that does not yet
	// maintain a global sequence (PG until PR-PG) leaves it zero on the way out.
	GlobalSeq int64
	Kind      EventKind     // required, must be Valid
	StepName  idutil.SafeID // required for step kinds; empty for CompensationStarted / terminal kinds
	Payload   []byte        // opaque JSON object-or-null; copied defensively by the Journal
	CreatedAt time.Time     // stamped by the Journal on Append
}

// MaxPayloadBytes caps Event.Payload size at append time so a misbehaving
// caller cannot push GB-scale JSON blobs into the event log. Sized to 64 KiB
// to match outbox per-entry payload caps; raise via ADR if a saga workload
// proves it insufficient.
const MaxPayloadBytes = 64 * 1024

// ValidateForAppend checks an Event submitted to Journal.Append. It is a pure
// validator (mutates nothing) enforcing:
//   - Kind is Valid and is NOT a terminal kind (terminal events are written only
//     by MarkTerminal, atomically with the projection's terminal flip);
//   - a step kind carries a present, valid StepName;
//   - Payload is a JSON object or null (or empty), within MaxPayloadBytes.
//
// It does NOT check phase legality (whether the kind is permitted in the
// instance's current status) — the Journal enforces that under its lock, where
// the current status is known.
func (e Event) ValidateForAppend() error {
	if !e.Kind.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: event has invalid Kind",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("kind=%d", e.Kind))))
	}
	if e.Kind.IsTerminal() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: terminal event must be written via MarkTerminal, not Append",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("kind=%s", e.Kind))))
	}
	if len(e.Payload) > MaxPayloadBytes {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: event payload exceeds MaxPayloadBytes",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("payloadBytes=%d max=%d", len(e.Payload), MaxPayloadBytes))))
	}
	if err := e.validateStepName(); err != nil {
		return err
	}
	return validateObjectOrNull(e.Payload)
}

func (e Event) validateStepName() error {
	if e.Kind.isStepKind() {
		if e.StepName == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga journal: step event missing StepName",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("kind=%s", e.Kind))))
		}
		if err := e.StepName.Validate(); err != nil {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"saga journal: step event has invalid StepName")
		}
	}
	return nil
}

// validateObjectOrNull accepts an empty payload, JSON null, or a JSON object;
// it rejects valid-but-non-object JSON (arrays, scalars) and malformed JSON.
// Fail-closed: anything not provably an object-or-null is rejected, mirroring
// the audit ledger's payload discipline.
func validateObjectOrNull(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if !json.Valid(payload) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: event payload is not valid JSON")
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: event payload must be a JSON object or null")
}
