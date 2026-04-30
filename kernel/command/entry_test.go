package command

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// DeadlineFor tests
// ---------------------------------------------------------------------------

func TestDeadlineFor_ZeroMeansNoTimeout(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  Timeouts{}, // all zero
	}
	assert.True(t, entry.DeadlineFor(PhaseScheduleToSend).IsZero())
	assert.True(t, entry.DeadlineFor(PhaseSendToComplete).IsZero())
	assert.True(t, entry.DeadlineFor(PhaseOverall).IsZero())
}

func TestDeadlineFor_ScheduleToSend(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: created,
		Timeouts:  Timeouts{ScheduleToSend: 30 * time.Second},
	}
	want := created.Add(30 * time.Second)
	assert.Equal(t, want, entry.DeadlineFor(PhaseScheduleToSend))
}

func TestDeadlineFor_SendToComplete(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sentAt := created.Add(10 * time.Second)
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: created,
		SentAt:    &sentAt,
		Timeouts:  Timeouts{SendToComplete: 5 * time.Minute},
	}
	want := sentAt.Add(5 * time.Minute)
	assert.Equal(t, want, entry.DeadlineFor(PhaseSendToComplete))
}

func TestDeadlineFor_SendToComplete_NotYetSent(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		SentAt:    nil, // not yet sent
		Timeouts:  Timeouts{SendToComplete: 5 * time.Minute},
	}
	// SentAt is nil — deadline cannot be computed.
	assert.True(t, entry.DeadlineFor(PhaseSendToComplete).IsZero())
}

func TestDeadlineFor_Overall(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: created,
		Timeouts:  Timeouts{OverallDeadline: 1 * time.Hour},
	}
	want := created.Add(1 * time.Hour)
	assert.Equal(t, want, entry.DeadlineFor(PhaseOverall))
}

func TestDeadlineFor_UnknownPhase(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  Timeouts{OverallDeadline: 1 * time.Hour},
	}
	assert.True(t, entry.DeadlineFor(TimeoutPhase(99)).IsZero())
}

func TestDeadlineFor_ZeroPhase(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:        "cmd-1",
		CreatedAt: time.Now(),
		Timeouts:  Timeouts{OverallDeadline: 1 * time.Hour},
	}
	assert.True(t, entry.DeadlineFor(TimeoutPhase(0)).IsZero(),
		"zero-value TimeoutPhase must return zero Time")
}

// ---------------------------------------------------------------------------
// ValidateNew tests
// ---------------------------------------------------------------------------

func TestValidateNew_Valid(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{"force":true}`),
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}
	assert.NoError(t, entry.ValidateNew())
}

func TestValidateNew_MissingID(t *testing.T) {
	t.Parallel()
	entry := Entry{
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_MissingDeviceID(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_MissingCommandType(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:        "cmd-1",
		DeviceID:  "dev-1",
		Payload:   []byte(`{}`),
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_MissingPayload(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_InvalidStatus(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      Status(0), // zero = invalid
		CreatedAt:   time.Now(),
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_NegativeTimeout(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      StatusPending,
		CreatedAt:   time.Now(),
		Timeouts:    Timeouts{ScheduleToSend: -1 * time.Second},
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
}

func TestValidateNew_NegativeTimeouts_AllFields(t *testing.T) {
	t.Parallel()
	base := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}

	tests := []struct {
		name     string
		timeouts Timeouts
	}{
		{"negative ScheduleToSend", Timeouts{ScheduleToSend: -1 * time.Second}},
		{"negative SendToComplete", Timeouts{SendToComplete: -1 * time.Second}},
		{"negative OverallDeadline", Timeouts{OverallDeadline: -1 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entry := base
			entry.Timeouts = tt.timeouts
			err := entry.ValidateNew()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
		})
	}
}

func TestValidateNew_NonPendingStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, now)
	entry.Status = StatusSent // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Pending status")
}

func TestValidateNew_NonZeroAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, now)
	entry.Attempt = 1 // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Attempt=0")
}

func TestValidateNew_HasPhaseTimestamps(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, now)
	entry.SentAt = &now // violate invariant
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "phase timestamps")
}

func TestValidateNew_MissingCreatedAt(t *testing.T) {
	t.Parallel()
	entry := Entry{
		ID:          "cmd-1",
		DeviceID:    "dev-1",
		CommandType: "reboot",
		Payload:     []byte(`{}`),
		Status:      StatusPending,
		// CreatedAt zero value
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
	assert.Contains(t, err.Error(), "CreatedAt")
}

func TestValidateNew_MetadataKeyCount_Exceeds(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entry := Entry{
		ID: "cmd-1", DeviceID: "dev-1", CommandType: "reboot",
		Payload: []byte(`{}`), Status: StatusPending, CreatedAt: now,
	}
	entry.Metadata = make(map[string]string)
	for i := range MaxMetadataKeys + 1 {
		entry.Metadata[fmt.Sprintf("key-%d", i)] = "v"
	}
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key count")
}

func TestValidateNew_MetadataWithinLimits(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entry := Entry{
		ID: "cmd-1", DeviceID: "dev-1", CommandType: "reboot",
		Payload: []byte(`{}`), Status: StatusPending, CreatedAt: now,
		Metadata: map[string]string{"device_region": "us-west-2"},
	}
	assert.NoError(t, entry.ValidateNew())
}

func TestValidateNew_NilMetadata_OK(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entry := Entry{
		ID: "cmd-1", DeviceID: "dev-1", CommandType: "reboot",
		Payload: []byte(`{}`), Status: StatusPending, CreatedAt: now,
	}
	assert.NoError(t, entry.ValidateNew())
}

// ---------------------------------------------------------------------------
// NewEntry tests
// ---------------------------------------------------------------------------

func TestNewEntry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{
		OverallDeadline: 1 * time.Hour,
	}, now)
	assert.Equal(t, "cmd-1", entry.ID)
	assert.Equal(t, "dev-1", entry.DeviceID)
	assert.Equal(t, "reboot", entry.CommandType)
	assert.Equal(t, StatusPending, entry.Status)
	assert.Equal(t, 0, entry.Attempt)
	assert.Nil(t, entry.SentAt)
	assert.Nil(t, entry.DeliveredAt)
	assert.Nil(t, entry.CompletedAt)
	assert.Equal(t, now, entry.CreatedAt, "CreatedAt must equal injected now, not wall-clock")
	assert.NoError(t, entry.ValidateNew())
}

func TestNewEntry_ExplicitTime(t *testing.T) {
	t.Parallel()
	// Verify CreatedAt is exactly the injected time, not time.Now().
	fixed := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, fixed)
	assert.Equal(t, fixed, entry.CreatedAt,
		"CreatedAt must be the injected time parameter, not wall-clock time.Now()")
}

func TestNewEntry_ZeroTime_FailsValidateNew(t *testing.T) {
	t.Parallel()
	// Constructing via NewEntry with zero time is accepted (no error return),
	// but ValidateNew catches the invalid CreatedAt. This is by design: the
	// constructor is a pure value builder; validation is a separate step.
	entry := NewEntry("cmd-1", "dev-1", "reboot", []byte(`{}`), Timeouts{}, time.Time{})
	err := entry.ValidateNew()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "CreatedAt")
}
