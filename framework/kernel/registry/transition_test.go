package registry

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
)

// TestLegalTransitions_Frozen pins the full edge set as a golden literal. A
// silently added / removed / re-targeted edge fails this test (anti-vacuity for
// the transition-table single source of truth). rejected/retired are terminal
// (absent from the table), mirroring saga statusTransitions.
func TestLegalTransitions_Frozen(t *testing.T) {
	t.Parallel()
	want := map[RegistrationState][]RegistrationState{
		StateSubmitted():       {StateProbing()},
		StateProbing():         {StateConformant(), StateRejected()},
		StateConformant():      {StatePendingApproval()},
		StatePendingApproval(): {StateApproved(), StateRejected()},
		StateApproved():        {StateActive()},
		StateActive():          {StateRetired()},
		// StateRejected / StateRetired: terminal, no outgoing edges (absent).
	}
	if !reflect.DeepEqual(legalTransitions, want) {
		t.Fatalf("legalTransitions = %v, want %v", legalTransitions, want)
	}
	// Anti-vacuity: every key + target must be a registered (non-zero) state.
	for from, targets := range legalTransitions {
		if !from.isRegistered() {
			t.Errorf("transition key %q is not a registered state", from)
		}
		for _, to := range targets {
			if !to.isRegistered() {
				t.Errorf("transition %q→%q targets an unregistered state", from, to)
			}
		}
	}
}

// TestTransition_HappyPath verifies the full main path is legal end to end.
func TestTransition_HappyPath(t *testing.T) {
	t.Parallel()
	path := []RegistrationState{
		StateSubmitted(), StateProbing(), StateConformant(),
		StatePendingApproval(), StateApproved(), StateActive(), StateRetired(),
	}
	for i := 0; i+1 < len(path); i++ {
		if err := Transition(path[i], path[i+1]); err != nil {
			t.Errorf("main-path %q→%q rejected: %v", path[i], path[i+1], err)
		}
		if !CanTransition(path[i], path[i+1]) {
			t.Errorf("CanTransition(%q,%q) = false, want true", path[i], path[i+1])
		}
	}
}

// TestTransition_TerminalBranches verifies the two →rejected branches are legal.
func TestTransition_TerminalBranches(t *testing.T) {
	t.Parallel()
	for _, from := range []RegistrationState{StateProbing(), StatePendingApproval()} {
		if err := Transition(from, StateRejected()); err != nil {
			t.Errorf("terminal branch %q→rejected rejected: %v", from, err)
		}
	}
}

// TestTransition_ActiveOnlyFromApproved is the headline approval-bypass
// invariant: active is reachable ONLY from approved. Every other source →active
// must be rejected with ErrRegistrationInvalidTransition.
func TestTransition_ActiveOnlyFromApproved(t *testing.T) {
	t.Parallel()
	for _, from := range allRegistrationStates {
		err := Transition(from, StateActive())
		if from == StateApproved() {
			if err != nil {
				t.Errorf("approved→active must be legal, got %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q→active must be rejected (approval-bypass invariant)", from)
			continue
		}
		errcodetest.AssertCode(t, err, errcode.ErrRegistrationInvalidTransition)
	}
}

// TestTransition_TerminalsHaveNoOutgoing verifies rejected/retired permit no
// transition to any state.
func TestTransition_TerminalsHaveNoOutgoing(t *testing.T) {
	t.Parallel()
	for _, from := range []RegistrationState{StateRejected(), StateRetired()} {
		for _, to := range allRegistrationStates {
			if err := Transition(from, to); err == nil {
				t.Errorf("terminal %q→%q must be rejected", from, to)
			}
		}
	}
}

// TestTransition_NoSelfLoops verifies no state may transition to itself
// (idempotent re-advance is rejected — intentional, mirrors saga).
func TestTransition_NoSelfLoops(t *testing.T) {
	t.Parallel()
	for _, s := range allRegistrationStates {
		if err := Transition(s, s); err == nil {
			t.Errorf("self-loop %q→%q must be rejected", s, s)
		}
	}
}

// TestTransition_ZeroTargetRejected verifies a forged zero target/source is
// rejected (zero is never a registered state, so the table lookup misses).
func TestTransition_ZeroTargetRejected(t *testing.T) {
	t.Parallel()
	var zero RegistrationState
	if err := Transition(StateSubmitted(), zero); err == nil {
		t.Error("transition to zero target must be rejected")
	}
	if err := Transition(zero, StateSubmitted()); err == nil {
		t.Error("transition from zero source must be rejected")
	}
}
