package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// EventKind classifies a durable saga journal event.
//
// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid kind, so a
// forgotten/uninitialised Event.Kind never silently appears valid — the same
// discipline as saga.Status.
type EventKind uint8

const (
	KindStepStarted     EventKind = iota + 1 // a forward step began executing
	KindStepCompleted                        // a forward step committed successfully
	KindStepFailed                           // a forward step failed
	KindStepCompensated                      // a previously committed step was rolled back
	KindSagaTerminal                         // the saga reached a terminal status (written via MarkTerminal only)
)

// Valid reports whether k is a recognized EventKind.
func (k EventKind) Valid() bool {
	return k >= KindStepStarted && k <= KindSagaTerminal
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
	case KindSagaTerminal:
		return "saga_terminal"
	default:
		return fmt.Sprintf("eventkind(%d)", k)
	}
}

// isStepKind reports whether k describes a per-step event (the kinds that
// require a StepName). KindSagaTerminal is saga-scoped, not step-scoped.
func (k EventKind) isStepKind() bool {
	switch k {
	case KindStepStarted, KindStepCompleted, KindStepFailed, KindStepCompensated:
		return true
	default:
		return false
	}
}

// Event is one durable, append-only record in a saga instance's history. The
// owning instance is identified by the Append(instanceID, ...) argument, so it
// is deliberately not duplicated as a field here. Version is assigned by
// Journal.Append (monotonic per instance, starting at 1) and CreatedAt is
// stamped by the Journal from its injected clock; callers leave both zero on
// the way in.
type Event struct {
	Version   int64         // assigned by Append; zero on input
	Kind      EventKind     // required, must be Valid
	StepName  idutil.SafeID // required for step kinds; empty for KindSagaTerminal
	Payload   []byte        // opaque JSON object-or-null; copied defensively by the Journal
	CreatedAt time.Time     // stamped by the Journal on Append
}

// ValidateForAppend checks an Event submitted to Journal.Append. It is a pure
// validator (mutates nothing) enforcing:
//   - Kind is Valid and is NOT KindSagaTerminal (terminal events are written
//     only by MarkTerminal, atomically with the projection flip);
//   - a step kind carries a present, valid StepName; a non-step kind carries
//     no StepName;
//   - Payload is a JSON object or null (or empty).
func (e Event) ValidateForAppend() error {
	if !e.Kind.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: event has invalid Kind",
			errcode.WithInternal(fmt.Sprintf("kind=%d", e.Kind)))
	}
	if e.Kind == KindSagaTerminal {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: KindSagaTerminal must be written via MarkTerminal, not Append")
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
				errcode.WithInternal(fmt.Sprintf("kind=%s", e.Kind)))
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
