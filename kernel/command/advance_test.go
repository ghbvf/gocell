package command

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

func TestAdvanceCommand_NilEntry(t *testing.T) {
	t.Parallel()
	err := AdvanceCommand(nil, StatusSent, time.Now())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Contains(t, err.Error(), "nil")
}

func TestAdvanceCommand_PendingToSent(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)

	err := AdvanceCommand(&entry, StatusSent, now)
	assert.NoError(t, err)
	assert.Equal(t, StatusSent, entry.Status)
	assert.Equal(t, 1, entry.Attempt)
	assert.NotNil(t, entry.SentAt)
	assert.Equal(t, now, *entry.SentAt)
	assert.Nil(t, entry.DeliveredAt)
	assert.Nil(t, entry.CompletedAt)
}

func TestAdvanceCommand_SentToDelivered(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))

	deliveredAt := now.Add(testtime.D5s)
	err := AdvanceCommand(&entry, StatusDelivered, deliveredAt)
	assert.NoError(t, err)
	assert.Equal(t, StatusDelivered, entry.Status)
	assert.NotNil(t, entry.DeliveredAt)
	assert.Equal(t, deliveredAt, *entry.DeliveredAt)
}

func TestAdvanceCommand_DeliveredToSucceeded(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.NoError(t, AdvanceCommand(&entry, StatusDelivered, now.Add(testtime.D1s)))

	completedAt := now.Add(testtime.D10s)
	err := AdvanceCommand(&entry, StatusSucceeded, completedAt)
	assert.NoError(t, err)
	assert.Equal(t, StatusSucceeded, entry.Status)
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, completedAt, *entry.CompletedAt)
}

func TestAdvanceCommand_InvalidTransition(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	err := AdvanceCommand(&entry, StatusSucceeded, time.Now())
	assert.Error(t, err)
	assert.Equal(t, StatusPending, entry.Status, "status must not change on invalid transition")
}

func TestAdvanceCommand_DeliveredWithoutSentAt(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	// Force Sent status without SentAt (simulating a corrupt entry).
	entry.Status = StatusSent
	entry.SentAt = nil

	err := AdvanceCommand(&entry, StatusDelivered, time.Now())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SentAt")
}

func TestAdvanceCommand_FullLifecycle(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "cert-renew", []byte(`{}`), Timeouts{
		ScheduleToSend:  testtime.D30s,
		SendToComplete:  testtime.D5min,
		OverallDeadline: testtime.D1h,
	}, created)
	now := created.Add(testtime.D5s)

	// Pending → Sent
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Sent → Delivered
	assert.NoError(t, AdvanceCommand(&entry, StatusDelivered, now.Add(testtime.D1s)))

	// Delivered → Succeeded
	assert.NoError(t, AdvanceCommand(&entry, StatusSucceeded, now.Add(testtime.D10s)))
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)

	// Terminal → any must fail
	err := AdvanceCommand(&entry, StatusFailed, now.Add(testtime.D20s))
	assert.Error(t, err)
}

func TestAdvanceCommand_ExpiredViaDeadline(t *testing.T) {
	t.Parallel()
	// Documents the intended adapter sweep pattern for L4 deadline enforcement:
	// 1. Create entry with OverallDeadline
	// 2. Advance to Sent
	// 3. Simulate now exceeding the overall deadline
	// 4. Adapter calls AdvanceCommand(StatusExpired) when DeadlineFor < now
	// 5. Assert CompletedAt is set
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "cert-renew", []byte(`{}`), Timeouts{
		OverallDeadline: testtime.D1min,
	}, created)

	// Advance to Sent
	sentAt := created.Add(testtime.D5s)
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, sentAt))

	// Simulate adapter sweep: now exceeds the overall deadline
	now := created.Add(testtime.D2min) // well past 1-minute deadline
	deadline := entry.DeadlineFor(PhaseOverall)
	assert.False(t, deadline.IsZero(), "OverallDeadline must produce a non-zero deadline")
	assert.True(t, now.After(deadline), "now must exceed the overall deadline")

	// Adapter would call AdvanceCommand to expire the command
	err := AdvanceCommand(&entry, StatusExpired, now)
	assert.NoError(t, err)
	assert.Equal(t, StatusExpired, entry.Status)
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, now, *entry.CompletedAt)
}

func TestAdvanceCommand_SentIncrementsAttempt(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)

	// First attempt
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Use ResetForRetry instead of direct mutation
	assert.NoError(t, ResetForRetry(&entry))

	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now.Add(testtime.D1s)))
	assert.Equal(t, 2, entry.Attempt)
}

func TestAdvanceCommand_AfterRetry(t *testing.T) {
	t.Parallel()
	// Full cycle: Pending→Sent→ResetForRetry→Pending→Sent — verify Attempt=2.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)

	// First attempt
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.Equal(t, 1, entry.Attempt)

	// Reset for retry
	assert.NoError(t, ResetForRetry(&entry))
	assert.Equal(t, StatusPending, entry.Status)
	assert.Equal(t, 1, entry.Attempt, "Attempt preserved across reset")

	// Second attempt
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now.Add(testtime.D1s)))
	assert.Equal(t, 2, entry.Attempt)

	// Complete successfully
	assert.NoError(t, AdvanceCommand(&entry, StatusDelivered, now.Add(testtime.D2s)))
	assert.NoError(t, AdvanceCommand(&entry, StatusSucceeded, now.Add(testtime.D3s)))
	assert.True(t, entry.Status.IsTerminal())
}

// ---------------------------------------------------------------------------
// ResetForRetry tests
// ---------------------------------------------------------------------------

func TestResetForRetry_NilEntry(t *testing.T) {
	t.Parallel()
	err := ResetForRetry(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Contains(t, err.Error(), "nil")
}

func TestResetForRetry_FromSent(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{"force":true}`), Timeouts{
		OverallDeadline: testtime.D1h,
	}, created)

	// Advance to Sent (Attempt becomes 1, SentAt set)
	sentAt := created.Add(testtime.D5s)
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, sentAt))
	assert.Equal(t, 1, entry.Attempt)
	assert.NotNil(t, entry.SentAt)

	// Reset for retry
	err := ResetForRetry(&entry)
	assert.NoError(t, err)
	assert.Equal(t, StatusPending, entry.Status)
	assert.Nil(t, entry.SentAt, "SentAt must be cleared")
	assert.Nil(t, entry.DeliveredAt, "DeliveredAt must be cleared")
	assert.Nil(t, entry.CompletedAt, "CompletedAt must be cleared")
	assert.Equal(t, 1, entry.Attempt, "Attempt must be preserved (not reset)")
}

func TestResetForRetry_FromFailed(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)

	// Advance through Sent → Failed
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.NoError(t, AdvanceCommand(&entry, StatusFailed, now.Add(testtime.D10s)))
	assert.True(t, entry.Status.IsTerminal())
	assert.NotNil(t, entry.CompletedAt)
	assert.Equal(t, 1, entry.Attempt)

	// Reset for retry from Failed
	err := ResetForRetry(&entry)
	assert.NoError(t, err)
	assert.Equal(t, StatusPending, entry.Status)
	assert.Nil(t, entry.SentAt, "SentAt must be cleared")
	assert.Nil(t, entry.CompletedAt, "CompletedAt must be cleared")
	assert.Equal(t, 1, entry.Attempt, "Attempt must be preserved")
}

func TestResetForRetry_FromTerminal_Rejected(t *testing.T) {
	t.Parallel()
	// Succeeded, Expired, Canceled are NOT retryable (unlike Failed).
	rejectedStatuses := []Status{StatusSucceeded, StatusExpired, StatusCanceled}
	for _, status := range rejectedStatuses {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
			// Force the status (bypassing state machine for test setup)
			entry.Status = status
			completedAt := created.Add(testtime.D10s)
			entry.CompletedAt = &completedAt

			err := ResetForRetry(&entry)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
			assert.Equal(t, status, entry.Status, "status must not change on rejected reset")
		})
	}
}

func TestResetForRetry_FromPending_Rejected(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)

	err := ResetForRetry(&entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Equal(t, StatusPending, entry.Status, "status must not change")
}

func TestResetForRetry_FromDelivered_Rejected(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D5s)

	// Advance to Delivered
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, now))
	assert.NoError(t, AdvanceCommand(&entry, StatusDelivered, now.Add(testtime.D1s)))

	err := ResetForRetry(&entry)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Equal(t, StatusDelivered, entry.Status, "status must not change")
}

func TestAdvanceCommand_ClockRegression_Rejected(t *testing.T) {
	t.Parallel()
	// Defensive test: if SentAt is already set (meaning it was never cleared by
	// ResetForRetry — a programming error in an adapter) and now precedes SentAt,
	// AdvanceCommand must return an error to prevent backwards timestamps.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, created)
	now := created.Add(testtime.D10s)

	// Manually set SentAt to simulate a corrupt/un-reset entry while keeping
	// status as Pending (as if a buggy adapter skipped ResetForRetry).
	entry.SentAt = &now // SentAt is set but entry is still Pending

	earlier := created.Add(testtime.D5s) // earlier than SentAt
	err := AdvanceCommand(&entry, StatusSent, earlier)
	assert.Error(t, err, "advancing with now < previous SentAt must be rejected (clock skew guard)")
	assert.Contains(t, err.Error(), "clock skew")
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestResetForRetry_PreservesFields(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := map[string]string{"env": "prod", "region": "us-east-1"}
	timeouts := Timeouts{
		ScheduleToSend:  testtime.D30s,
		SendToComplete:  testtime.D5min,
		OverallDeadline: testtime.D1h,
	}
	entry := NewEntry("cmd-42", "dev-99", "cert-renew", []byte(`{"key":"val"}`), timeouts, created)
	entry.Metadata = metadata

	// Advance to Sent
	sentAt := created.Add(testtime.D5s)
	assert.NoError(t, AdvanceCommand(&entry, StatusSent, sentAt))

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
