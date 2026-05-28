package saga

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// legalEdges enumerates every allowed (from → to) status transition.
// It is the single source of truth shared by status_test.go and advance_test.go.
var legalEdges = []struct{ from, to Status }{
	{StatusPending, StatusRunning},
	{StatusPending, StatusFailed},
	{StatusPending, StatusExpired},
	{StatusRunning, StatusSucceeded},
	{StatusRunning, StatusCompensating},
	{StatusRunning, StatusFailed},
	{StatusRunning, StatusExpired},
	{StatusCompensating, StatusCompensated},
	{StatusCompensating, StatusCompensationFailed},
	{StatusCompensating, StatusExpired},
}

func allStatuses() []Status {
	return []Status{
		StatusPending, StatusRunning, StatusCompensating,
		StatusSucceeded, StatusFailed, StatusCompensated, StatusExpired,
		StatusCompensationFailed,
	}
}

func isLegal(from, to Status) bool {
	for _, e := range legalEdges {
		if e.from == from && e.to == to {
			return true
		}
	}
	return false
}

func legalFrom(from Status) []Status {
	var out []Status
	for _, e := range legalEdges {
		if e.from == from {
			out = append(out, e.to)
		}
	}
	return out
}

func TestStatus_ZeroValueIsNotValid(t *testing.T) {
	t.Parallel()
	var zero Status
	assert.False(t, zero.Valid(), "zero-value Status must not be valid")
	assert.Equal(t, "invalid", zero.String(), "zero-value Status.String() must return \"invalid\"")
}

func TestStatus_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    Status
		want bool
	}{
		{Status(0), false},
		{StatusPending, true},
		{StatusRunning, true},
		{StatusCompensating, true},
		{StatusSucceeded, true},
		{StatusFailed, true},
		{StatusCompensated, true},
		{StatusExpired, true},
		{StatusCompensationFailed, true},
		{Status(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.Valid())
		})
	}
}

func TestStatus_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    Status
		want string
	}{
		{Status(0), "invalid"},
		{StatusPending, "pending"},
		{StatusRunning, "running"},
		{StatusCompensating, "compensating"},
		{StatusSucceeded, "succeeded"},
		{StatusFailed, "failed"},
		{StatusCompensated, "compensated"},
		{StatusExpired, "expired"},
		{StatusCompensationFailed, "compensation_failed"},
		{Status(99), "status(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.String())
		})
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    Status
		want bool
	}{
		{StatusPending, false},
		{StatusRunning, false},
		{StatusCompensating, false},
		{StatusSucceeded, true},
		{StatusFailed, true},
		{StatusCompensated, true},
		{StatusExpired, true},
		{StatusCompensationFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.s.IsTerminal())
		})
	}
}

func TestStatus_CanTransitionTo_Matrix(t *testing.T) {
	t.Parallel()
	for _, from := range allStatuses() {
		for _, to := range allStatuses() {
			t.Run(from.String()+"->"+to.String(), func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, isLegal(from, to), from.CanTransitionTo(to))
			})
		}
	}
}

func TestStatus_ValidTransitions_TerminalReturnsNil(t *testing.T) {
	t.Parallel()
	for _, s := range []Status{StatusSucceeded, StatusFailed, StatusCompensated, StatusExpired, StatusCompensationFailed} {
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Nil(t, s.ValidTransitions())
		})
	}
}

func TestStatus_ValidTransitions_DefensiveCopy(t *testing.T) {
	t.Parallel()
	got := StatusPending.ValidTransitions()
	require.Len(t, got, len(legalFrom(StatusPending)))
	got[0] = StatusSucceeded // mutate caller's copy; internal table must be unaffected
	assert.True(t, StatusPending.CanTransitionTo(StatusRunning),
		"mutating a returned slice must not corrupt the internal transition table")
	assert.Len(t, StatusPending.ValidTransitions(), len(legalFrom(StatusPending)))
}

func TestTransition_Legal(t *testing.T) {
	t.Parallel()
	for _, e := range legalEdges {
		t.Run(e.from.String()+"->"+e.to.String(), func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, Transition(e.from, e.to))
		})
	}
}

func TestTransition_Illegal(t *testing.T) {
	t.Parallel()
	for _, from := range allStatuses() {
		for _, to := range allStatuses() {
			if isLegal(from, to) {
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
