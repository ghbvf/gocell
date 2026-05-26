package outbox

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/fsm"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// State is the publication state of a transactional-outbox entry. It is the
// single Go-side source of truth for the wire/DB status strings: String()
// produces the exact value bound into the `status` column, so adapters/postgres
// and runtime/outbox must use the enum rather than bare string literals
// (enforced by the OUTBOX-STATE-LITERAL-BAN-01 archtest).
//
// Unlike the maturity Phase in cellvocab, outbox entries genuinely transition
// at runtime: the relay claims a pending entry, then settles it to published,
// dead, or back to pending (retry). The transition table + TransitionState
// model that real machine, mirroring kernel/saga/status.go and
// kernel/command/status.go.
//
// ref: kernel/saga/status.go (transition-table pattern).
type State uint8

const (
	// StatePending: awaiting publish (initial state and the retry back-off
	// state).
	//
	// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid State, so a
	// forgotten/uninitialised State will not silently appear as pending.
	StatePending   State = iota + 1 // = 1
	StateClaiming                   // locked by a relay instance, publish in progress
	StatePublished                  // delivered to broker (terminal)
	StateDead                       // exceeded MaxAttempts, needs manual intervention (terminal)
)

// Valid reports whether s is a recognized State value.
func (s State) Valid() bool {
	return s >= StatePending && s <= StateDead
}

// String returns the canonical wire/DB status string. This is the sole
// sanctioned producer of the status literal; see OUTBOX-STATE-LITERAL-BAN-01.
func (s State) String() string {
	switch s {
	case 0:
		return "invalid"
	case StatePending:
		return "pending"
	case StateClaiming:
		return "claiming"
	case StatePublished:
		return "published"
	case StateDead:
		return "dead"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}

// IsTerminal reports whether s is a terminal (final) state: published or dead.
func (s State) IsTerminal() bool {
	switch s {
	case StatePublished, StateDead:
		return true
	default:
		return false
	}
}

// ParseState parses a wire/DB status string into a State.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseState(s string) (State, error) {
	switch s {
	case "pending":
		return StatePending, nil
	case "claiming":
		return StateClaiming, nil
	case "published":
		return StatePublished, nil
	case "dead":
		return StateDead, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid outbox entry state",
			errcode.WithInternal(fmt.Sprintf("value=%q", s)))
	}
}

// ---------------------------------------------------------------------------
// Transition table
// ---------------------------------------------------------------------------

// stateTransitions maps each non-terminal state to its valid target states.
//
//   - Pending → Claiming: the relay's ClaimPending atomically locks the entry.
//   - Claiming → Published: publish succeeded (MarkPublished).
//   - Claiming → Dead: attempts exhausted (MarkDead / ReclaimStale terminal).
//   - Claiming → Pending: transient failure / stale-lease reclaim, back to the
//     queue with back-off (MarkRetry / ReclaimStale).
//
// Terminal states (Published, Dead) are absent → fsm yields nil → deny all.
var stateTransitions = map[State][]State{
	StatePending:  {StateClaiming},
	StateClaiming: {StatePublished, StateDead, StatePending},
}

// CanTransitionTo reports whether s can transition to target.
func (s State) CanTransitionTo(target State) bool {
	return fsm.CanTransition(stateTransitions, s, target)
}

// ValidTransitions returns the states reachable from s, as a defensive copy.
// Returns nil for terminal states.
func (s State) ValidTransitions() []State {
	return fsm.AllowedTargets(stateTransitions, s)
}

// TransitionState validates a state transition from → to and returns an error
// if it is not allowed. Pure validation; it does NOT mutate any state.
// Callers invoke the corresponding store mutation (MarkPublished / MarkDead /
// MarkRetry) after a successful return; the relay calls this at its settlement
// decision points so an illegal target fails loudly.
//
// Scope: this guard validates the relay's Go-side settlement decisions —
// the three paths where the relay explicitly decides claiming→published,
// claiming→dead, or claiming→pending. The store's ClaimPending
// (pending→claiming) and ReclaimStale (claiming→pending/dead) transitions are
// enforced by SQL CAS on the status column (WHERE status='claiming' / 'pending'),
// not by a Go-side decision point. Wiring TransitionState at those SQL sites
// would be a tautological assertion that cannot fail in practice. The complete
// legal transition graph is instead validated statically by the
// OUTBOX-STATE-TRANSITION-COMPLETENESS-01 archtest, which verifies that every
// State const is present in stateTransitions or documented as terminal.
func TransitionState(from, to State) error {
	if from.CanTransitionTo(to) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"outbox: invalid entry state transition",
		errcode.WithInternal(fmt.Sprintf("from=%s to=%s", from, to)))
}
