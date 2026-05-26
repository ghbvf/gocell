package outbox

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/fsm"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// State is the publication state of a transactional-outbox entry.
//
// # Single-source role
//
// State is the single Go-side source of the wire/DB status strings: String()
// produces the exact value bound into the `status` column, so adapters/postgres
// and runtime/outbox must use the enum rather than bare string literals
// (enforced by the OUTBOX-STATE-LITERAL-BAN-01 archtest).
//
// # Transition table
//
// stateTransitions documents the complete legal state graph and is validated by
// the OUTBOX-STATE-TRANSITION-COMPLETENESS-01 archtest (every State const must
// be a key). It is NOT the runtime enforcement source for SQL operations:
//
//   - relay settlement (writeBackOne / handleFailedEntry): three paths where the
//     relay explicitly decides claiming→published, claiming→dead, or
//     claiming→pending. Each calls Transition as a Mark↔target cross-check:
//     a function that calls MarkPublished must also Transition to StatePublished,
//     catching copy-paste mismatches at CI time (OUTBOX-STATE-TRANSITION-GUARD-01).
//     These calls are not correctness guards — the constant arguments mean they
//     can never fail at runtime — but they serve as machine-readable pairing
//     assertions.
//
//   - store ClaimPending (pending→claiming) and ReclaimStale
//     (claiming→pending/dead): no Mark* pairing exists for these transitions.
//     Their correctness is enforced by SQL CAS predicates
//     (WHERE status='claiming'/'pending'), conformance behavior tests, and
//     LITERAL-BAN. Duplicating Transition calls there would add no
//     pairing-check value and would be misleading.
//
//   - SQL-text ↔ table-edge machine binding: SQL args are ...any, so the wire
//     string cannot be type-constrained at the binding site. This is a true
//     Medium ceiling, explicitly accepted; no fragile static SQL↔edge parser
//     is attempted.
//
// ref: kernel/saga/status.go (transition-table + Transition pattern).
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

// stateTransitions maps every State to its valid target states. Terminal states
// are explicit keys with empty slices — their absence of outgoing edges is
// intentional, not an oversight. The fsm helpers treat empty-slice and absent
// identically (both deny all transitions), so the behavioral contract is the
// same; the explicit keys make the documentation machine-verifiable by
// OUTBOX-STATE-TRANSITION-COMPLETENESS-01.
//
//   - Pending → Claiming: the relay's ClaimPending atomically locks the entry.
//   - Claiming → Published: publish succeeded (MarkPublished).
//   - Claiming → Dead: attempts exhausted (MarkDead / ReclaimStale terminal).
//   - Claiming → Pending: transient failure / stale-lease reclaim, back to the
//     queue with back-off (MarkRetry / ReclaimStale).
//   - Published: terminal — no outgoing transitions.
//   - Dead: terminal — no outgoing transitions.
var stateTransitions = map[State][]State{
	StatePending:   {StateClaiming},
	StateClaiming:  {StatePublished, StateDead, StatePending},
	StatePublished: {}, // terminal — no outgoing transitions
	StateDead:      {}, // terminal — no outgoing transitions
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

// Transition validates a state transition from → to and returns an error if it
// is not allowed. Pure validation; it does NOT mutate any state.
//
// # Usage in the relay settlement loop
//
// The relay calls Transition at its three settlement decision points as a
// Mark↔target cross-check: a function that calls MarkPublished must also call
// Transition(_, StatePublished), and similarly for MarkDead/StateDead and
// MarkRetry/StatePending. This pairing is enforced statically by
// OUTBOX-STATE-TRANSITION-GUARD-01. Because the arguments are constants, these
// calls cannot fail at runtime — their purpose is machine-readable pairing
// documentation, not runtime correctness.
//
// # Store transitions without Transition calls
//
// ClaimPending (pending→claiming) and ReclaimStale (claiming→pending/dead) have
// no Mark* pairing to cross-check. Their correctness is enforced by SQL CAS
// predicates, conformance behavior tests, and LITERAL-BAN.
//
// ref: kernel/saga/status.go::Transition (same pattern and name).
func Transition(from, to State) error {
	if from.CanTransitionTo(to) {
		return nil
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"outbox: invalid entry state transition",
		errcode.WithInternal(fmt.Sprintf("from=%s to=%s", from, to)))
}
