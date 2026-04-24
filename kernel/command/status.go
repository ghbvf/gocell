package command

import (
	"fmt"
	"slices"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Status represents the lifecycle state of an L4 command entry.
// L4 commands target devices with long-latency round-trips (IoT command ack,
// certificate renewal, etc.).
//
// ref: ThingsBoard RPC status model (QUEUED→SENT→DELIVERED→SUCCESSFUL);
// ref: Temporal Nexus operations (SCHEDULED→STARTED→terminal, three-tier timeouts).
type Status uint8

const (
	// StatusPending: enqueued, awaiting send to device transport.
	//
	// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid status.
	// A forgotten/uninitialised Entry.Status will not silently appear valid.
	StatusPending   Status = iota + 1 // = 1
	StatusSent                        // transmitted to device transport
	StatusDelivered                   // device ACK'd receipt (not execution)
	StatusSucceeded                   // device confirmed successful execution
	StatusFailed                      // permanent failure (device error or retries exhausted)
	StatusExpired                     // deadline elapsed before completion
	StatusCanceled                    // explicitly canceled by operator/system
)

// Valid reports whether s is a recognised Status value.
func (s Status) Valid() bool {
	return s >= StatusPending && s <= StatusCanceled
}

// String returns a human-readable label for the Status.
func (s Status) String() string {
	switch s {
	case 0:
		return "invalid"
	case StatusPending:
		return "pending"
	case StatusSent:
		return "sent"
	case StatusDelivered:
		return "delivered"
	case StatusSucceeded:
		return "succeeded"
	case StatusFailed:
		return "failed"
	case StatusExpired:
		return "expired"
	case StatusCanceled:
		return "canceled"
	default:
		return fmt.Sprintf("status(%d)", s)
	}
}

// IsTerminal reports whether s is a terminal (final) state.
// Terminal states: Succeeded, Failed, Expired, Canceled.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled:
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
// Design note: StatusSent → StatusSucceeded is allowed without passing through
// StatusDelivered. This covers the "Ack(Success) without prior Report" flow —
// devices that execute synchronously within the Dequeue lease can ack directly,
// leaving DeliveredAt nil as a signal that no intermediate Report was observed.
// This intentionally loosens strict Temporal-style staging (where
// ScheduledTime/StartedTime/CompletedTime are always distinct events) in favour
// of simpler device integration where Report is optional.
//
// ref: Temporal activity lifecycle — StartedTime is optional when using
// synchronous activities; CompletedTime can follow ScheduledTime directly.
var statusTransitions = map[Status][]Status{
	StatusPending:   {StatusSent, StatusFailed, StatusExpired, StatusCanceled},
	StatusSent:      {StatusDelivered, StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled},
	StatusDelivered: {StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled},
	// Terminal states have no outgoing transitions.
}

// CanTransitionTo reports whether s can transition to target.
func (s Status) CanTransitionTo(target Status) bool {
	return slices.Contains(statusTransitions[s], target)
}

// ValidTransitions returns the set of states reachable from s.
// Returns nil for terminal states.
func (s Status) ValidTransitions() []Status {
	targets := statusTransitions[s]
	if len(targets) == 0 {
		return nil
	}
	out := make([]Status, len(targets))
	copy(out, targets)
	return out
}

// Transition validates a state transition from → to and returns an error
// if the transition is not allowed. This is a pure validation function;
// it does NOT mutate any state.
func Transition(from, to Status) error {
	if from.CanTransitionTo(to) {
		return nil
	}
	return errcode.New(errcode.ErrValidationFailed,
		fmt.Sprintf("command: invalid transition %s -> %s", from, to))
}
