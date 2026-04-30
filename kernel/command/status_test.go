package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		{StatusSent, true},
		{StatusDelivered, true},
		{StatusSucceeded, true},
		{StatusFailed, true},
		{StatusExpired, true},
		{StatusCanceled, true},
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
		{StatusSent, "sent"},
		{StatusDelivered, "delivered"},
		{StatusSucceeded, "succeeded"},
		{StatusFailed, "failed"},
		{StatusExpired, "expired"},
		{StatusCanceled, "canceled"},
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
		s        Status
		terminal bool
	}{
		{StatusPending, false},
		{StatusSent, false},
		{StatusDelivered, false},
		{StatusSucceeded, true},
		{StatusFailed, true},
		{StatusExpired, true},
		{StatusCanceled, true},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.terminal, tt.s.IsTerminal())
		})
	}
}

func TestCanTransitionTo_AllValid(t *testing.T) {
	t.Parallel()
	validPairs := []struct {
		from, to Status
	}{
		// From Pending
		{StatusPending, StatusSent},
		{StatusPending, StatusFailed},
		{StatusPending, StatusExpired},
		{StatusPending, StatusCanceled},
		// From Sent
		{StatusSent, StatusDelivered},
		{StatusSent, StatusSucceeded},
		{StatusSent, StatusFailed},
		{StatusSent, StatusExpired},
		{StatusSent, StatusCanceled},
		// From Delivered
		{StatusDelivered, StatusSucceeded},
		{StatusDelivered, StatusFailed},
		{StatusDelivered, StatusExpired},
		{StatusDelivered, StatusCanceled},
	}
	for _, tt := range validPairs {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestCanTransitionTo_InvalidFromTerminal(t *testing.T) {
	t.Parallel()
	terminals := []Status{StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled}
	allStatuses := []Status{
		StatusPending, StatusSent, StatusDelivered,
		StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled,
	}
	for _, from := range terminals {
		for _, to := range allStatuses {
			name := from.String() + "->" + to.String()
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				assert.False(t, from.CanTransitionTo(to),
					"terminal state %s must not transition to %s", from, to)
			})
		}
	}
}

func TestCanTransitionTo_InvalidSelfTransition(t *testing.T) {
	t.Parallel()
	allStatuses := []Status{
		StatusPending, StatusSent, StatusDelivered,
		StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled,
	}
	for _, s := range allStatuses {
		name := s.String() + "->" + s.String()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, s.CanTransitionTo(s),
				"self-transition %s->%s must be invalid", s, s)
		})
	}
}

func TestCanTransitionTo_InvalidSkip(t *testing.T) {
	t.Parallel()
	invalidSkips := []struct {
		from, to Status
	}{
		{StatusPending, StatusSucceeded},
		{StatusPending, StatusDelivered},
	}
	for _, tt := range invalidSkips {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestCanTransitionTo_InvalidReverse(t *testing.T) {
	t.Parallel()
	reverses := []struct {
		from, to Status
	}{
		{StatusDelivered, StatusPending},
		{StatusDelivered, StatusSent},
	}
	for _, tt := range reverses {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestTransition_Valid(t *testing.T) {
	t.Parallel()
	err := Transition(StatusPending, StatusSent)
	assert.NoError(t, err)

	err = Transition(StatusSent, StatusDelivered)
	assert.NoError(t, err)

	err = Transition(StatusDelivered, StatusSucceeded)
	assert.NoError(t, err)
}

func TestTransition_Invalid(t *testing.T) {
	t.Parallel()
	err := Transition(StatusPending, StatusSucceeded)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")

	err = Transition(StatusSucceeded, StatusFailed)
	assert.Error(t, err)
}

func TestValidTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from Status
		want []Status
	}{
		{StatusPending, []Status{StatusSent, StatusFailed, StatusExpired, StatusCanceled}},
		{StatusSent, []Status{StatusDelivered, StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled}},
		{StatusDelivered, []Status{StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled}},
		{StatusSucceeded, nil},
		{StatusFailed, nil},
		{StatusExpired, nil},
		{StatusCanceled, nil},
	}
	for _, tt := range tests {
		t.Run(tt.from.String(), func(t *testing.T) {
			t.Parallel()
			got := tt.from.ValidTransitions()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCanTransitionTo_ZeroValueFrom(t *testing.T) {
	t.Parallel()
	allStatuses := []Status{
		StatusPending, StatusSent, StatusDelivered,
		StatusSucceeded, StatusFailed, StatusExpired, StatusCanceled,
	}
	for _, to := range allStatuses {
		t.Run(to.String(), func(t *testing.T) {
			t.Parallel()
			assert.False(t, Status(0).CanTransitionTo(to),
				"zero-value Status must not transition to %s", to)
		})
	}
}
