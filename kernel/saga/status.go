package saga

import (
	"fmt"
	"slices"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Status represents the lifecycle state of an L3 (WorkflowEventual) saga
// instance. A saga orchestrates a sequence of steps with compensation: on
// forward failure it rolls back committed steps in reverse.
//
// ref: dtm-labs/dtm saga branch state; temporalio/temporal workflow state.
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
	StatusFailed                         // could not / should not compensate (terminal)
	StatusCompensated                    // forward failed, rollback completed cleanly (terminal)
	StatusExpired                        // overall timeout elapsed (terminal)
)

// Valid reports whether s is a recognized Status value.
func (s Status) Valid() bool {
	return s >= StatusPending && s <= StatusExpired
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
	default:
		return fmt.Sprintf("status(%d)", s)
	}
}

// IsTerminal reports whether s is a terminal (final) state.
// Terminal states: Succeeded, Failed, Compensated, Expired.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCompensated, StatusExpired:
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
//   - Compensating → Failed: a Compensate action itself failed (second-order
//     failure). The saga cannot reach a clean Compensated state, so it
//     terminates in Failed for dead-letter / ops intervention. Failed is thus
//     reachable from two states by design.
//   - There is no Compensating → Running: compensation is monotonic, never
//     resuming forward work.
//   - Expired is reachable from every non-terminal state: the overall timeout
//     (owned by the Coordinator) is orthogonal to step progress.
var statusTransitions = map[Status][]Status{
	StatusPending:      {StatusRunning, StatusFailed, StatusExpired},
	StatusRunning:      {StatusSucceeded, StatusCompensating, StatusFailed, StatusExpired},
	StatusCompensating: {StatusCompensated, StatusFailed, StatusExpired},
	// Terminal states have no outgoing transitions.
}

// CanTransitionTo reports whether s can transition to target.
func (s Status) CanTransitionTo(target Status) bool {
	return slices.Contains(statusTransitions[s], target)
}

// ValidTransitions returns the set of states reachable from s.
// Returns nil for terminal states. The returned slice is a defensive copy;
// mutating it does not affect the internal transition table.
func (s Status) ValidTransitions() []Status {
	targets := statusTransitions[s]
	if len(targets) == 0 {
		return nil
	}
	out := make([]Status, len(targets))
	copy(out, targets)
	return out
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
		errcode.WithInternal(fmt.Sprintf("from=%s to=%s", from, to)))
}
