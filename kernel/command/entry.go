package command

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TimeoutPhase identifies which phase of the command lifecycle a deadline
// applies to.
type TimeoutPhase uint8

const (
	// PhaseScheduleToSend: max duration from creation (Pending) to Sent.
	PhaseScheduleToSend TimeoutPhase = iota + 1
	// PhaseSendToComplete: max duration from Sent to a terminal state.
	PhaseSendToComplete
	// PhaseOverall: absolute max duration from creation to terminal state.
	PhaseOverall
)

// Timeouts configures per-phase deadlines for an L4 command.
// Zero duration means no timeout for that phase.
//
// Adapter responsibility: the kernel defines timeout configuration and
// pure deadline calculation (via Entry.DeadlineFor). The adapter
// MUST run a periodic sweeper (e.g., every 30s–60s) that queries
// non-terminal commands, computes DeadlineFor for each active phase,
// and calls AdvanceStatus(..., StatusExpired, now) when now exceeds
// the deadline.
//
// ref: Temporal Nexus operations — ScheduleToCloseTimeout, ScheduleToStartTimeout,
// StartToCloseTimeout. GoCell simplifies to three tiers.
type Timeouts struct {
	// ScheduleToSend: max wait from creation to device transport delivery.
	ScheduleToSend time.Duration
	// SendToComplete: max wait from Sent to terminal state (Succeeded/Failed).
	SendToComplete time.Duration
	// OverallDeadline: absolute max from creation to any terminal state.
	OverallDeadline time.Duration
}

// Entry represents a single L4 command enqueued for device execution.
//
// The lifecycle: Pending → Sent → Delivered → Succeeded/Failed/Expired/Canceled.
// Adapters persist and advance the status via StateAdvancer.
type Entry struct {
	ID          string
	DeviceID    string
	CommandType string // e.g., "reboot", "cert-renew", "config-push"
	Payload     []byte
	Status      Status
	Metadata    map[string]string // extensible key-value pairs

	Timeouts Timeouts

	// Attempt tracks the current delivery attempt (0 = first attempt).
	// Design note: there is no explicit "Retrying" status. Retry is modelled
	// as the same Pending→Sent arc with an incremented Attempt counter. This
	// avoids a combinatorial explosion of states (Retrying×{Sent,Delivered})
	// and keeps the transition table compact. Adapters inspect Attempt to
	// decide whether to retry or transition to Failed.
	//
	// Use ResetForRetry to move a command back to Pending for retry —
	// do NOT directly mutate Status/SentAt fields.
	Attempt     int
	CreatedAt   time.Time
	SentAt      *time.Time // set when Status transitions to Sent
	DeliveredAt *time.Time // set when device ACKs receipt
	CompletedAt *time.Time // set when terminal state reached
}

// DeadlineFor returns the absolute deadline time for the given timeout phase.
// Returns zero Time if no timeout is configured for that phase, or if the
// prerequisite timestamp is not yet set (e.g., PhaseSendToComplete before Sent).
//
// This is a pure calculation — adapter code compares the result to time.Now().
func (e *Entry) DeadlineFor(phase TimeoutPhase) time.Time {
	switch phase {
	case PhaseScheduleToSend:
		if e.Timeouts.ScheduleToSend <= 0 {
			return time.Time{}
		}
		return e.CreatedAt.Add(e.Timeouts.ScheduleToSend)

	case PhaseSendToComplete:
		if e.Timeouts.SendToComplete <= 0 || e.SentAt == nil {
			return time.Time{}
		}
		return e.SentAt.Add(e.Timeouts.SendToComplete)

	case PhaseOverall:
		if e.Timeouts.OverallDeadline <= 0 {
			return time.Time{}
		}
		return e.CreatedAt.Add(e.Timeouts.OverallDeadline)

	default:
		return time.Time{}
	}
}

// ValidateNew checks that required fields are present, status is valid,
// timeouts are non-negative, and creation-time invariants hold.
// This is a creation-time validator — it enforces that the entry is in its
// initial state (Pending, no timestamps, Attempt=0). It is NOT suitable for
// validating an entry that has been advanced through the lifecycle.
//
// Creation-time invariants (enforced by NewEntry):
//   - Status must be StatusPending
//   - SentAt, DeliveredAt, CompletedAt must be nil
//   - Attempt must be 0
//
// These constraints ensure that callers cannot bypass the state machine
// by constructing an Entry with an arbitrary status or timestamps.
func (e *Entry) ValidateNew() error {
	if e.ID == "" {
		return errcode.New(errcode.ErrValidationFailed, "command: entry missing ID")
	}
	if e.DeviceID == "" {
		return errcode.New(errcode.ErrValidationFailed, "command: entry missing DeviceID")
	}
	if e.CommandType == "" {
		return errcode.New(errcode.ErrValidationFailed, "command: entry missing CommandType")
	}
	if len(e.Payload) == 0 {
		return errcode.New(errcode.ErrValidationFailed, "command: entry missing Payload")
	}
	if !e.Status.Valid() {
		return errcode.New(errcode.ErrValidationFailed, "command: entry has invalid Status")
	}
	if e.Status != StatusPending {
		return errcode.New(errcode.ErrValidationFailed, "command: new entry must have Pending status")
	}
	if e.CreatedAt.IsZero() {
		return errcode.New(errcode.ErrValidationFailed, "command: entry missing CreatedAt")
	}
	if e.SentAt != nil || e.DeliveredAt != nil || e.CompletedAt != nil {
		return errcode.New(errcode.ErrValidationFailed, "command: new entry must not have phase timestamps (SentAt/DeliveredAt/CompletedAt)")
	}
	if e.Attempt != 0 {
		return errcode.New(errcode.ErrValidationFailed, "command: new entry must have Attempt=0")
	}
	if e.Timeouts.ScheduleToSend < 0 {
		return errcode.New(errcode.ErrValidationFailed, "command: ScheduleToSend timeout must be non-negative")
	}
	if e.Timeouts.SendToComplete < 0 {
		return errcode.New(errcode.ErrValidationFailed, "command: SendToComplete timeout must be non-negative")
	}
	if e.Timeouts.OverallDeadline < 0 {
		return errcode.New(errcode.ErrValidationFailed, "command: OverallDeadline timeout must be non-negative")
	}
	if err := validateMetadata(e.Metadata); err != nil {
		return err
	}
	return nil
}

// NewEntry creates an Entry in Pending status with the given parameters.
// This is the only sanctioned way to create a command entry; it enforces:
//   - Status = StatusPending (callers cannot create non-Pending commands)
//   - SentAt / DeliveredAt / CompletedAt = nil (no phase timestamps at creation)
//   - Attempt = 0
//   - CreatedAt = now (caller-provided, not wall-clock)
//
// The now parameter makes the constructor pure and deterministically testable.
//
// ref: Temporal StartWorkflowExecution — callers submit intent + timeouts,
// status/timestamps are owned by the server. Time is an explicit event
// parameter, never read from wall clock inside the state machine.
func NewEntry(id, deviceID, commandType string, payload []byte, timeouts Timeouts, now time.Time) Entry {
	return Entry{
		ID:          id,
		DeviceID:    deviceID,
		CommandType: commandType,
		Payload:     payload,
		Status:      StatusPending,
		Timeouts:    timeouts,
		Attempt:     0,
		CreatedAt:   now,
	}
}
