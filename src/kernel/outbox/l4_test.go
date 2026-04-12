package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// CommandStatus — enum tests
// ---------------------------------------------------------------------------

func TestCommandStatus_ZeroValueIsNotValid(t *testing.T) {
	var zero CommandStatus
	assert.False(t, zero.Valid(), "zero-value CommandStatus must not be valid")
	assert.Equal(t, "invalid", zero.String(), "zero-value CommandStatus.String() must return \"invalid\"")
}

func TestCommandStatus_Valid(t *testing.T) {
	tests := []struct {
		s    CommandStatus
		want bool
	}{
		{CommandStatus(0), false},
		{CommandPending, true},
		{CommandSent, true},
		{CommandDelivered, true},
		{CommandSucceeded, true},
		{CommandFailed, true},
		{CommandExpired, true},
		{CommandCanceled, true},
		{CommandStatus(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.Valid())
		})
	}
}

func TestCommandStatus_String(t *testing.T) {
	tests := []struct {
		s    CommandStatus
		want string
	}{
		{CommandStatus(0), "invalid"},
		{CommandPending, "pending"},
		{CommandSent, "sent"},
		{CommandDelivered, "delivered"},
		{CommandSucceeded, "succeeded"},
		{CommandFailed, "failed"},
		{CommandExpired, "expired"},
		{CommandCanceled, "canceled"},
		{CommandStatus(99), "command_status(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.String())
		})
	}
}

func TestCommandStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		s        CommandStatus
		terminal bool
	}{
		{CommandPending, false},
		{CommandSent, false},
		{CommandDelivered, false},
		{CommandSucceeded, true},
		{CommandFailed, true},
		{CommandExpired, true},
		{CommandCanceled, true},
	}
	for _, tt := range tests {
		t.Run(tt.s.String(), func(t *testing.T) {
			assert.Equal(t, tt.terminal, tt.s.IsTerminal())
		})
	}
}

// ---------------------------------------------------------------------------
// Transition table tests
// ---------------------------------------------------------------------------

func TestCanTransitionTo_AllValid(t *testing.T) {
	// Every valid (from, to) pair must return true.
	validPairs := []struct {
		from, to CommandStatus
	}{
		// From Pending
		{CommandPending, CommandSent},
		{CommandPending, CommandExpired},
		{CommandPending, CommandCanceled},
		// From Sent
		{CommandSent, CommandDelivered},
		{CommandSent, CommandFailed},
		{CommandSent, CommandExpired},
		{CommandSent, CommandCanceled},
		// From Delivered
		{CommandDelivered, CommandSucceeded},
		{CommandDelivered, CommandFailed},
		{CommandDelivered, CommandExpired},
		{CommandDelivered, CommandCanceled},
	}
	for _, tt := range validPairs {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			assert.True(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestCanTransitionTo_InvalidFromTerminal(t *testing.T) {
	terminals := []CommandStatus{CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled}
	allStatuses := []CommandStatus{
		CommandPending, CommandSent, CommandDelivered,
		CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled,
	}
	for _, from := range terminals {
		for _, to := range allStatuses {
			name := from.String() + "->" + to.String()
			t.Run(name, func(t *testing.T) {
				assert.False(t, from.CanTransitionTo(to),
					"terminal state %s must not transition to %s", from, to)
			})
		}
	}
}

func TestCanTransitionTo_InvalidSelfTransition(t *testing.T) {
	allStatuses := []CommandStatus{
		CommandPending, CommandSent, CommandDelivered,
		CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled,
	}
	for _, s := range allStatuses {
		name := s.String() + "->" + s.String()
		t.Run(name, func(t *testing.T) {
			assert.False(t, s.CanTransitionTo(s),
				"self-transition %s->%s must be invalid", s, s)
		})
	}
}

func TestCanTransitionTo_InvalidSkip(t *testing.T) {
	// Must not skip intermediate states.
	invalidSkips := []struct {
		from, to CommandStatus
	}{
		{CommandPending, CommandSucceeded},
		{CommandPending, CommandDelivered},
		{CommandPending, CommandFailed},
		{CommandSent, CommandSucceeded},
	}
	for _, tt := range invalidSkips {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			assert.False(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestCanTransitionTo_InvalidReverse(t *testing.T) {
	reverses := []struct {
		from, to CommandStatus
	}{
		{CommandSent, CommandPending},
		{CommandDelivered, CommandPending},
		{CommandDelivered, CommandSent},
	}
	for _, tt := range reverses {
		name := tt.from.String() + "->" + tt.to.String()
		t.Run(name, func(t *testing.T) {
			assert.False(t, tt.from.CanTransitionTo(tt.to))
		})
	}
}

func TestTransition_Valid(t *testing.T) {
	err := Transition(CommandPending, CommandSent)
	assert.NoError(t, err)

	err = Transition(CommandSent, CommandDelivered)
	assert.NoError(t, err)

	err = Transition(CommandDelivered, CommandSucceeded)
	assert.NoError(t, err)
}

func TestTransition_Invalid(t *testing.T) {
	err := Transition(CommandPending, CommandSucceeded)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")

	err = Transition(CommandSucceeded, CommandFailed)
	assert.Error(t, err)
}

func TestValidTransitions(t *testing.T) {
	tests := []struct {
		from CommandStatus
		want []CommandStatus
	}{
		{CommandPending, []CommandStatus{CommandSent, CommandExpired, CommandCanceled}},
		{CommandSent, []CommandStatus{CommandDelivered, CommandFailed, CommandExpired, CommandCanceled}},
		{CommandDelivered, []CommandStatus{CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled}},
		{CommandSucceeded, nil},
		{CommandFailed, nil},
		{CommandExpired, nil},
		{CommandCanceled, nil},
	}
	for _, tt := range tests {
		t.Run(tt.from.String(), func(t *testing.T) {
			got := tt.from.ValidTransitions()
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Timeout / Deadline tests
// ---------------------------------------------------------------------------

func TestCommandTimeouts_ZeroMeansNoTimeout(t *testing.T) {
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  CommandTimeouts{}, // all zero
	}
	assert.True(t, entry.DeadlineFor(PhaseScheduleToSend).IsZero())
	assert.True(t, entry.DeadlineFor(PhaseSendToComplete).IsZero())
	assert.True(t, entry.DeadlineFor(PhaseOverall).IsZero())
}

func TestDeadlineFor_ScheduleToSend(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: created,
		Timeouts:  CommandTimeouts{ScheduleToSend: 30 * time.Second},
	}
	want := created.Add(30 * time.Second)
	assert.Equal(t, want, entry.DeadlineFor(PhaseScheduleToSend))
}

func TestDeadlineFor_SendToComplete(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sentAt := created.Add(10 * time.Second)
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: created,
		SentAt:    &sentAt,
		Timeouts:  CommandTimeouts{SendToComplete: 5 * time.Minute},
	}
	want := sentAt.Add(5 * time.Minute)
	assert.Equal(t, want, entry.DeadlineFor(PhaseSendToComplete))
}

func TestDeadlineFor_SendToComplete_NotYetSent(t *testing.T) {
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		SentAt:    nil, // not yet sent
		Timeouts:  CommandTimeouts{SendToComplete: 5 * time.Minute},
	}
	// SentAt is nil — deadline cannot be computed.
	assert.True(t, entry.DeadlineFor(PhaseSendToComplete).IsZero())
}

func TestDeadlineFor_Overall(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: created,
		Timeouts:  CommandTimeouts{OverallDeadline: 1 * time.Hour},
	}
	want := created.Add(1 * time.Hour)
	assert.Equal(t, want, entry.DeadlineFor(PhaseOverall))
}

func TestDeadlineFor_UnknownPhase(t *testing.T) {
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  CommandTimeouts{OverallDeadline: 1 * time.Hour},
	}
	assert.True(t, entry.DeadlineFor(TimeoutPhase(99)).IsZero())
}

// ---------------------------------------------------------------------------
// CommandEntry.Validate tests
// ---------------------------------------------------------------------------

func TestCommandEntry_Validate_Valid(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{"force":true}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	assert.NoError(t, entry.Validate())
}

func TestCommandEntry_Validate_MissingID(t *testing.T) {
	entry := CommandEntry{
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_Validate_MissingDeviceID(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_Validate_MissingCommandType(t *testing.T) {
	entry := CommandEntry{
		ID:       "cmd-1",
		DeviceID: "dev-1",
		Payload:  []byte(`{}`),
		Status:   CommandPending,
		CreatedAt: time.Now(),
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_Validate_MissingPayload(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_Validate_InvalidStatus(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandStatus(0), // zero = invalid
		CreatedAt:   time.Now(),
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_Validate_NegativeTimeout(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
		Timeouts:    CommandTimeouts{ScheduleToSend: -1 * time.Second},
	}
	err := entry.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

// ---------------------------------------------------------------------------
// Interface compliance (compile-time)
// ---------------------------------------------------------------------------

type mockCommandWriter struct{}

func (m *mockCommandWriter) WriteCommand(_ context.Context, _ CommandEntry) error { return nil }

var _ CommandWriter = (*mockCommandWriter)(nil)

type mockCommandReader struct{}

func (m *mockCommandReader) PendingCommands(_ context.Context, _ string) ([]CommandEntry, error) {
	return nil, nil
}
func (m *mockCommandReader) GetCommand(_ context.Context, _ string) (*CommandEntry, error) {
	return nil, nil
}

var _ CommandReader = (*mockCommandReader)(nil)

type mockCommandStateAdvancer struct{}

func (m *mockCommandStateAdvancer) AdvanceStatus(_ context.Context, _ string, _, _ CommandStatus) error {
	return nil
}

var _ CommandStateAdvancer = (*mockCommandStateAdvancer)(nil)

// ---------------------------------------------------------------------------
// Edge case tests (review findings F5, F11)
// ---------------------------------------------------------------------------

func TestCanTransitionTo_ZeroValueFrom(t *testing.T) {
	// Zero-value CommandStatus(0) must not transition to anything.
	allStatuses := []CommandStatus{
		CommandPending, CommandSent, CommandDelivered,
		CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled,
	}
	for _, to := range allStatuses {
		assert.False(t, CommandStatus(0).CanTransitionTo(to),
			"zero-value CommandStatus must not transition to %s", to)
	}
}

func TestDeadlineFor_ZeroPhase(t *testing.T) {
	entry := CommandEntry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  CommandTimeouts{OverallDeadline: 1 * time.Hour},
	}
	assert.True(t, entry.DeadlineFor(TimeoutPhase(0)).IsZero(),
		"zero-value TimeoutPhase must return zero Time")
}

func TestCommandEntry_Validate_NegativeTimeouts_AllFields(t *testing.T) {
	base := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}

	tests := []struct {
		name     string
		timeouts CommandTimeouts
	}{
		{"negative ScheduleToSend", CommandTimeouts{ScheduleToSend: -1 * time.Second}},
		{"negative SendToComplete", CommandTimeouts{SendToComplete: -1 * time.Second}},
		{"negative OverallDeadline", CommandTimeouts{OverallDeadline: -1 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := base
			entry.Timeouts = tt.timeouts
			err := entry.Validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
		})
	}
}
