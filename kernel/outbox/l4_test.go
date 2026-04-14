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
// CommandEntry.ValidateNew tests (renamed from Validate — L4-API-01)
// ---------------------------------------------------------------------------

func TestCommandEntry_ValidateNew_Valid(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{"force":true}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	assert.NoError(t, entry.ValidateNew())
}

func TestCommandEntry_ValidateNew_MissingID(t *testing.T) {
	entry := CommandEntry{
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_ValidateNew_MissingDeviceID(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_ValidateNew_MissingCommandType(t *testing.T) {
	entry := CommandEntry{
		ID:        "cmd-1",
		DeviceID:  "dev-1",
		Payload:   []byte(`{}`),
		Status:    CommandPending,
		CreatedAt: time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_ValidateNew_MissingPayload(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Status:      CommandPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_ValidateNew_InvalidStatus(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandStatus(0), // zero = invalid
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestCommandEntry_ValidateNew_NegativeTimeout(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		CreatedAt:   time.Now(),
		Timeouts:    CommandTimeouts{ScheduleToSend: -1 * time.Second},
	}
	err := entry.ValidateNew()
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

// mockCommandStateAdvancer — updated to include now time.Time (L4-API-01)
type mockCommandStateAdvancer struct{}

func (m *mockCommandStateAdvancer) AdvanceStatus(_ context.Context, _ string, _, _ CommandStatus, _ time.Time) error {
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

func TestCommandEntry_ValidateNew_MissingCreatedAt(t *testing.T) {
	entry := CommandEntry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      CommandPending,
		// CreatedAt zero value
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Contains(t, err.Error(), "CreatedAt")
}

func TestCommandEntry_ValidateNew_NegativeTimeouts_AllFields(t *testing.T) {
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
			err := entry.ValidateNew()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateNew — creation-time invariant enforcement
// ---------------------------------------------------------------------------

func TestCommandEntry_ValidateNew_NonPendingStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, now)
	entry.Status = CommandSent // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Pending status")
}

func TestCommandEntry_ValidateNew_NonZeroAttempt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, now)
	entry.Attempt = 1 // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Attempt=0")
}

func TestCommandEntry_ValidateNew_HasPhaseTimestamps(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, now)
	entry.SentAt = &now // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "phase timestamps")
}

// ---------------------------------------------------------------------------
// NewCommandEntry — L4-PURE-01: now time.Time injection
// ---------------------------------------------------------------------------

func TestNewCommandEntry(t *testing.T) {
	now := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{
		OverallDeadline: 1 * time.Hour,
	}, now)
	assert.Equal(t, "cmd-1", entry.ID)
	assert.Equal(t, "dev-1", entry.DeviceID)
	assert.Equal(t, "reboot", entry.CommandType)
	assert.Equal(t, CommandPending, entry.Status)
	assert.Equal(t, 0, entry.Attempt)
	assert.Nil(t, entry.SentAt)
	assert.Nil(t, entry.DeliveredAt)
	assert.Nil(t, entry.CompletedAt)
	assert.Equal(t, now, entry.CreatedAt, "CreatedAt must equal injected now, not wall-clock")
	assert.NoError(t, entry.ValidateNew())
}

func TestNewCommandEntry_ExplicitTime(t *testing.T) {
	// L4-PURE-01: verify CreatedAt is exactly the injected time, not time.Now().
	fixed := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, fixed)
	assert.Equal(t, fixed, entry.CreatedAt,
		"CreatedAt must be the injected time parameter, not wall-clock time.Now()")
}

// ---------------------------------------------------------------------------
// AdvanceCommand
// ---------------------------------------------------------------------------

func TestAdvanceCommand_PendingToSent(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)

	err := AdvanceCommand(&entry, CommandSent, now)
	assert.NoError(t, err)
	assert.Equal(t, CommandSent, entry.Status)
	assert.Equal(t, 1, entry.Attempt)
	assert.NotNil(t, entry.SentAt)
	assert.Equal(t, now, *entry.SentAt)
	assert.Nil(t, entry.DeliveredAt)
	assert.Nil(t, entry.CompletedAt)
}

func TestAdvanceCommand_SentToDelivered(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))

	deliveredAt := now.Add(5 * time.Second)
	err := AdvanceCommand(&entry, CommandDelivered, deliveredAt)
	assert.NoError(t, err)
	assert.Equal(t, CommandDelivered, entry.Status)
	assert.NotNil(t, entry.DeliveredAt)
	assert.Equal(t, deliveredAt, *entry.DeliveredAt)
}

func TestAdvanceCommand_DeliveredToSucceeded(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.NoError(t, AdvanceCommand(&entry, CommandDelivered, now.Add(1*time.Second)))

	completedAt := now.Add(10 * time.Second)
	err := AdvanceCommand(&entry, CommandSucceeded, completedAt)
	assert.NoError(t, err)
	assert.Equal(t, CommandSucceeded, entry.Status)
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, completedAt, *entry.CompletedAt)
}

func TestAdvanceCommand_InvalidTransition(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	err := AdvanceCommand(&entry, CommandSucceeded, time.Now())
	assert.Error(t, err)
	assert.Equal(t, CommandPending, entry.Status, "status must not change on invalid transition")
}

func TestAdvanceCommand_DeliveredWithoutSentAt(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	// Force Sent status without SentAt (simulating a corrupt entry).
	entry.Status = CommandSent
	entry.SentAt = nil

	err := AdvanceCommand(&entry, CommandDelivered, time.Now())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SentAt")
}

func TestAdvanceCommand_FullLifecycle(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "cert-renew", []byte(`{}`), CommandTimeouts{
		ScheduleToSend:  30 * time.Second,
		SendToComplete:  5 * time.Minute,
		OverallDeadline: 1 * time.Hour,
	}, created)
	now := created.Add(5 * time.Second)

	// Pending → Sent
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Sent → Delivered
	assert.NoError(t, AdvanceCommand(&entry, CommandDelivered, now.Add(1*time.Second)))

	// Delivered → Succeeded
	assert.NoError(t, AdvanceCommand(&entry, CommandSucceeded, now.Add(10*time.Second)))
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)

	// Terminal → any must fail
	err := AdvanceCommand(&entry, CommandFailed, now.Add(20*time.Second))
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// AdvanceCommand — L4 delay arrival / deadline expiry (review S3-F3)
// ---------------------------------------------------------------------------

func TestAdvanceCommand_ExpiredViaDeadline(t *testing.T) {
	// Documents the intended adapter sweep pattern for L4 deadline enforcement:
	// 1. Create entry with OverallDeadline
	// 2. Advance to Sent
	// 3. Simulate now exceeding the overall deadline
	// 4. Adapter calls AdvanceCommand(CommandExpired) when DeadlineFor < now
	// 5. Assert CompletedAt is set
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "cert-renew", []byte(`{}`), CommandTimeouts{
		OverallDeadline: 1 * time.Minute,
	}, created)

	// Advance to Sent
	sentAt := created.Add(5 * time.Second)
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, sentAt))

	// Simulate adapter sweep: now exceeds the overall deadline
	now := created.Add(2 * time.Minute) // well past 1-minute deadline
	deadline := entry.DeadlineFor(PhaseOverall)
	assert.False(t, deadline.IsZero(), "OverallDeadline must produce a non-zero deadline")
	assert.True(t, now.After(deadline), "now must exceed the overall deadline")

	// Adapter would call AdvanceCommand to expire the command
	err := AdvanceCommand(&entry, CommandExpired, now)
	assert.NoError(t, err)
	assert.Equal(t, CommandExpired, entry.Status)
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, now, *entry.CompletedAt)
}

// ---------------------------------------------------------------------------
// ResetForRetry — L4-RETRY-01
// ---------------------------------------------------------------------------

func TestResetForRetry_FromSent(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{"force":true}`), CommandTimeouts{
		OverallDeadline: 1 * time.Hour,
	}, created)

	// Advance to Sent (Attempt becomes 1, SentAt set)
	sentAt := created.Add(5 * time.Second)
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, sentAt))
	assert.Equal(t, 1, entry.Attempt)
	assert.NotNil(t, entry.SentAt)

	// Reset for retry
	err := ResetForRetry(&entry)
	assert.NoError(t, err)
	assert.Equal(t, CommandPending, entry.Status)
	assert.Nil(t, entry.SentAt, "SentAt must be cleared")
	assert.Nil(t, entry.DeliveredAt, "DeliveredAt must be cleared")
	assert.Nil(t, entry.CompletedAt, "CompletedAt must be cleared")
	assert.Equal(t, 1, entry.Attempt, "Attempt must be preserved (not reset)")
}

func TestResetForRetry_FromFailed(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)

	// Advance through Sent → Failed
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.NoError(t, AdvanceCommand(&entry, CommandFailed, now.Add(10*time.Second)))
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, 1, entry.Attempt)

	// Reset for retry from Failed
	err := ResetForRetry(&entry)
	assert.NoError(t, err)
	assert.Equal(t, CommandPending, entry.Status)
	assert.Nil(t, entry.SentAt, "SentAt must be cleared")
	assert.Nil(t, entry.CompletedAt, "CompletedAt must be cleared")
	assert.Equal(t, 1, entry.Attempt, "Attempt must be preserved")
}

func TestResetForRetry_FromTerminal_Rejected(t *testing.T) {
	// Succeeded, Expired, Canceled are NOT retryable (unlike Failed).
	rejectedStatuses := []CommandStatus{CommandSucceeded, CommandExpired, CommandCanceled}
	for _, status := range rejectedStatuses {
		t.Run(status.String(), func(t *testing.T) {
			created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
			// Force the status (bypassing state machine for test setup)
			entry.Status = status
			completedAt := created.Add(10 * time.Second)
			entry.CompletedAt = &completedAt

			err := ResetForRetry(&entry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
			assert.Equal(t, status, entry.Status, "status must not change on rejected reset")
		})
	}
}

func TestResetForRetry_FromPending_Rejected(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)

	err := ResetForRetry(&entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Equal(t, CommandPending, entry.Status, "status must not change")
}

func TestResetForRetry_FromDelivered_Rejected(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)

	// Advance to Delivered
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.NoError(t, AdvanceCommand(&entry, CommandDelivered, now.Add(1*time.Second)))

	err := ResetForRetry(&entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Equal(t, CommandDelivered, entry.Status, "status must not change")
}

func TestResetForRetry_PreservesFields(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := map[string]string{"env": "prod", "region": "us-east-1"}
	timeouts := CommandTimeouts{
		ScheduleToSend:  30 * time.Second,
		SendToComplete:  5 * time.Minute,
		OverallDeadline: 1 * time.Hour,
	}
	entry := NewCommandEntry("cmd-42", "dev-99", "cert-renew", []byte(`{"key":"val"}`), timeouts, created)
	entry.Metadata = metadata

	// Advance to Sent
	sentAt := created.Add(5 * time.Second)
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, sentAt))

	// Reset
	assert.NoError(t, ResetForRetry(&entry))

	// Verify preserved fields
	assert.Equal(t, "cmd-42", entry.ID)
	assert.Equal(t, "dev-99", entry.DeviceID)
	assert.Equal(t, "cert-renew", entry.CommandType)
	assert.Equal(t, []byte(`{"key":"val"}`), entry.Payload)
	assert.Equal(t, timeouts, entry.Timeouts)
	assert.Equal(t, metadata, entry.Metadata)
	assert.Equal(t, created, entry.CreatedAt, "CreatedAt must be preserved")
}

func TestAdvanceCommand_AfterRetry(t *testing.T) {
	// Full cycle: Pending→Sent→ResetForRetry→Pending→Sent — verify Attempt=2.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)

	// First attempt
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Reset for retry (replaces old direct-mutation pattern)
	assert.NoError(t, ResetForRetry(&entry))
	assert.Equal(t, CommandPending, entry.Status)
	assert.Equal(t, 1, entry.Attempt, "Attempt preserved across reset")

	// Second attempt
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now.Add(1*time.Second)))
	assert.Equal(t, 2, entry.Attempt)

	// Complete successfully
	assert.NoError(t, AdvanceCommand(&entry, CommandDelivered, now.Add(2*time.Second)))
	assert.NoError(t, AdvanceCommand(&entry, CommandSucceeded, now.Add(3*time.Second)))
	assert.True(t, entry.Status.IsTerminal())
}

func TestAdvanceCommand_SentIncrementsAttempt(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewCommandEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), CommandTimeouts{}, created)
	now := created.Add(5 * time.Second)

	// First attempt
	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Use ResetForRetry instead of direct mutation (L4-RETRY-01)
	assert.NoError(t, ResetForRetry(&entry))

	assert.NoError(t, AdvanceCommand(&entry, CommandSent, now.Add(1*time.Second)))
	assert.Equal(t, 2, entry.Attempt)
}
