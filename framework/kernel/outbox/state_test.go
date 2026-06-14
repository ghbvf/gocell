package outbox

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// legalStateEdges enumerates every allowed (from → to) outbox entry transition.
// Single source of truth for the table-driven tests below; mirrors the
// kernel/saga/status_test.go convention.
var legalStateEdges = []struct{ from, to State }{
	{StatePending, StateClaiming},
	{StateClaiming, StatePublished},
	{StateClaiming, StateDead},
	{StateClaiming, StatePending}, // MarkRetry / ReclaimStale back-off
}

func allStates() []State {
	return []State{StatePending, StateClaiming, StatePublished, StateDead}
}

func isLegalState(from, to State) bool {
	for _, e := range legalStateEdges {
		if e.from == from && e.to == to {
			return true
		}
	}
	return false
}

func TestState_ZeroValueIsNotValid(t *testing.T) {
	t.Parallel()
	var zero State
	assert.False(t, zero.Valid(), "zero-value State must not be valid")
	assert.Equal(t, "invalid", zero.String())
}

func TestState_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    State
		want bool
	}{
		{State(0), false},
		{StatePending, true},
		{StateClaiming, true},
		{StatePublished, true},
		{StateDead, true},
		{State(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.Valid())
		})
	}
}

func TestState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    State
		want string
	}{
		{State(0), "invalid"},
		{StatePending, "pending"},
		{StateClaiming, "claiming"},
		{StatePublished, "published"},
		{StateDead, "dead"},
		{State(99), "state(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.String())
		})
	}
}

func TestParseState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    State
		wantErr bool
	}{
		{"pending", StatePending, false},
		{"claiming", StateClaiming, false},
		{"published", StatePublished, false},
		{"dead", StateDead, false},
		{"", 0, true},
		{"Pending", 0, true},
		{"unknown", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseState(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr))
				assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseState_RoundTrip locks String()↔ParseState as a single source: the
// wire string a State emits must parse back to the same State. This is the
// invariant the OUTBOX-STATE-LITERAL-BAN archtest protects (String() is the
// sole producer of the wire form).
func TestParseState_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, s := range allStates() {
		got, err := ParseState(s.String())
		require.NoError(t, err)
		assert.Equal(t, s, got)
	}
}

func TestState_IsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    State
		want bool
	}{
		{StatePending, false},
		{StateClaiming, false},
		{StatePublished, true},
		{StateDead, true},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.IsTerminal())
		})
	}
}

func TestState_CanTransitionTo_Matrix(t *testing.T) {
	t.Parallel()
	for _, from := range allStates() {
		for _, to := range allStates() {
			t.Run(from.String()+"->"+to.String(), func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, isLegalState(from, to), from.CanTransitionTo(to))
			})
		}
	}
}

func TestState_ValidTransitions_TerminalReturnsNil(t *testing.T) {
	t.Parallel()
	for _, s := range []State{StatePublished, StateDead} {
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Nil(t, s.ValidTransitions())
		})
	}
}

func TestState_ValidTransitions_DefensiveCopy(t *testing.T) {
	t.Parallel()
	got := StateClaiming.ValidTransitions()
	require.NotEmpty(t, got)
	got[0] = State(0) // mutate caller's copy; internal table must be unaffected
	assert.True(t, StateClaiming.CanTransitionTo(StatePublished),
		"mutating a returned slice must not corrupt the internal transition table")
}

func TestTransition_Legal(t *testing.T) {
	t.Parallel()
	for _, e := range legalStateEdges {
		t.Run(e.from.String()+"->"+e.to.String(), func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, Transition(e.from, e.to))
		})
	}
}

func TestTransition_Illegal(t *testing.T) {
	t.Parallel()
	for _, from := range allStates() {
		for _, to := range allStates() {
			if isLegalState(from, to) {
				continue
			}
			t.Run(from.String()+"->"+to.String(), func(t *testing.T) {
				t.Parallel()
				err := Transition(from, to)
				require.Error(t, err)
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr))
				assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
			})
		}
	}
}

// TestStateTransitions_TerminalAsKey verifies that terminal states are explicit
// keys in stateTransitions with empty slices. This documents that their absence
// from outgoing edges is intentional, not an oversight. The fsm helpers treat
// empty-slice and absent identically (both deny all transitions), so the
// behavioral contract is unchanged — only the documentation is made explicit.
func TestStateTransitions_TerminalAsKey(t *testing.T) {
	t.Parallel()
	for _, s := range []State{StatePublished, StateDead} {
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			targets, exists := stateTransitions[s]
			assert.True(t, exists, "terminal state %s must be an explicit key in stateTransitions", s)
			assert.Empty(t, targets, "terminal state %s must have no outgoing transitions", s)
			// Behavioral contract: CanTransitionTo still returns false for all targets.
			for _, other := range allStates() {
				assert.False(t, s.CanTransitionTo(other),
					"terminal %s must not transition to %s", s, other)
			}
			// ValidTransitions returns nil (len==0 path in AllowedTargets).
			assert.Nil(t, s.ValidTransitions(), "terminal %s must return nil from ValidTransitions", s)
		})
	}
}
