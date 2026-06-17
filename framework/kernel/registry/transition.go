package registry

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/fsm"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// legalTransitions is the single source of truth for RegistrationState
// legality. It maps each non-terminal state to its valid target states; the
// frozen edge set encodes the state-machine invariants.
//
// Main path: submitted → probing → conformant → pending-approval → approved →
// active → retired.
//
// Design notes (invariants):
//   - probing → {conformant, rejected}: automated conformance either passes
//     (conformant) or fails into the terminal rejected branch.
//   - pending-approval → {approved, rejected}: an admin either approves or
//     rejects; rejection is terminal.
//   - approved → active: this is the ONLY edge into active. "Only approved can
//     activate" is the approval-bypass invariant (FR-003 / SC-002): a submitted /
//     probing / conformant / pending-approval contract cannot be activated.
//   - active → retired: an admin retires a live contract (terminal).
//   - rejected / retired are terminal: absent from the table (lookup miss → no
//     outgoing edge), mirroring kernel/saga statusTransitions. There is no
//     rejected → active edge — a rejected contract is never activatable.
//   - No self-loops: a state cannot transition to itself (idempotent re-advance
//     is rejected, consistent with saga).
var legalTransitions = map[RegistrationState][]RegistrationState{
	stateSubmitted:       {stateProbing},
	stateProbing:         {stateConformant, stateRejected},
	stateConformant:      {statePendingApproval},
	statePendingApproval: {stateApproved, stateRejected},
	stateApproved:        {stateActive},
	stateActive:          {stateRetired},
	// stateRejected / stateRetired: terminal, no outgoing transitions.
}

// CanTransition reports whether a transition from → to is permitted by the
// frozen legalTransitions table. A forged zero state (source or target) is never
// a registered key/target, so it is denied.
func CanTransition(from, to RegistrationState) bool {
	return fsm.CanTransition(legalTransitions, from, to)
}

// Transition validates a state transition from → to. It is a pure validation
// function (no mutation) and returns ErrRegistrationInvalidTransition when the
// transition is not permitted by legalTransitions — including any transition out
// of a terminal state, any self-loop, and any source/target that is the forged
// zero value.
func Transition(from, to RegistrationState) error {
	if CanTransition(from, to) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrRegistrationInvalidTransition,
		"registry: invalid registration state transition",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("from=%s to=%s", from, to))))
}
