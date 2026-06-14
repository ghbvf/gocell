package saga

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/fsm"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Status represents the lifecycle state of an L3 (WorkflowEventual) saga
// instance. A saga orchestrates a sequence of steps with compensation: on
// forward failure it rolls back committed steps in reverse.
//
// ref: dtm-labs/dtm saga branch state (CompensateFailed maps to StatusCompensationFailed);
// temporalio/temporal workflow state (no direct equivalent — Temporal compensates
// via activities/saga pattern, not built-in terminal status).
type Status uint8

const (
	// StatusPending: created, no step has run yet.
	//
	// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid status.
	// A forgotten/uninitialised Instance.Status will not silently appear valid.
	StatusPending      Status = iota + 1 // = 1
	StatusRunning                        // executing steps forward
	StatusCompensating                   // a step failed; running Compensate in reverse
	StatusSucceeded                      // all steps committed (terminal)
	StatusFailed                         // forward-phase failure with no rollback (never entered Compensating; terminal)
	StatusCompensated                    // forward failed, rollback completed cleanly (terminal)
	StatusExpired                        // overall timeout elapsed (terminal)
	// StatusCompensationFailed is reached when the compensation phase itself
	// encounters a step failure: at least one CompensateFunc returned a non-nil
	// error. It is distinct from StatusFailed (which signals a forward-phase
	// failure with no compensation, or an explicit no-compensate decision) so
	// operators can distinguish "rollback failed" from "forward failed" by
	// inspecting the terminal status alone, without reading the event log.
	// Reachable only from StatusCompensating (terminal).
	StatusCompensationFailed // = 8
)

// Valid reports whether s is a recognized Status value.
func (s Status) Valid() bool {
	return s >= StatusPending && s <= StatusCompensationFailed
}

// String returns a human-readable label for the Status.
func (s Status) String() string {
	switch s {
	case 0:
		return "invalid"
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusCompensating:
		return "compensating"
	case StatusSucceeded:
		return "succeeded"
	case StatusFailed:
		return "failed"
	case StatusCompensated:
		return "compensated"
	case StatusExpired:
		return "expired"
	case StatusCompensationFailed:
		return "compensation_failed"
	default:
		return fmt.Sprintf("status(%d)", s)
	}
}

// IsTerminal reports whether s is a terminal (final) state.
// Terminal states: Succeeded, Failed, Compensated, Expired, CompensationFailed.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCompensated, StatusExpired, StatusCompensationFailed:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Transition table
// ---------------------------------------------------------------------------

// statusTransitions maps each non-terminal state to its valid target states.
//
// Design notes:
//   - Running → Compensating: a forward step failed with ≥1 prior committed
//     step, so committed work must be undone in reverse. This is the saga's
//     defining edge. The Coordinator (PR-03) chooses Compensating vs Failed
//     based on whether any compensable step has committed.
//   - Running → Failed: failed before any step committed (nothing to undo) —
//     terminate directly, avoiding a no-op Compensating hop.
//   - Compensating → CompensationFailed: a Compensate action itself failed
//     (second-order failure). Distinct from StatusFailed so operators can
//     differentiate "rollback failed" (CompensationFailed) from "forward
//     failed, no rollback" (Failed) by reading the terminal status alone.
//   - StatusFailed is intentionally NOT reachable from StatusCompensating:
//     any second-order compensate failure must land in StatusCompensationFailed.
//     This makes the two root causes structurally unambiguous.
//   - There is no Compensating → Running: compensation is monotonic, never
//     resuming forward work.
//   - Expired is reachable from every non-terminal state: the overall timeout
//     (owned by the Coordinator) is orthogonal to step progress.
var statusTransitions = map[Status][]Status{
	StatusPending:      {StatusRunning, StatusFailed, StatusExpired},
	StatusRunning:      {StatusSucceeded, StatusCompensating, StatusFailed, StatusExpired},
	StatusCompensating: {StatusCompensated, StatusCompensationFailed, StatusExpired},
	// Terminal states have no outgoing transitions.
}

// CanTransitionTo reports whether s can transition to target.
func (s Status) CanTransitionTo(target Status) bool {
	return fsm.CanTransition(statusTransitions, s, target)
}

// ValidTransitions returns the set of states reachable from s, as a defensive
// copy. Returns nil for terminal states.
func (s Status) ValidTransitions() []Status {
	return fsm.AllowedTargets(statusTransitions, s)
}

// Transition validates a state transition from → to and returns an error if
// the transition is not allowed. This is a pure validation function; it does
// NOT mutate any state.
func Transition(from, to Status) error {
	if from.CanTransitionTo(to) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga: invalid status transition",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("from=%s to=%s", from, to))))
}
