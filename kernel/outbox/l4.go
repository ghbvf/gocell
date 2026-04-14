package outbox

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// CommandStatus — L4 (Device Latent) command lifecycle states
// ---------------------------------------------------------------------------

// CommandStatus represents the lifecycle state of an L4 command entry.
// L4 commands target devices with long-latency round-trips (IoT command ack,
// certificate renewal, etc.).
//
// ref: ThingsBoard RPC status model (QUEUED→SENT→DELIVERED→SUCCESSFUL);
// ref: Temporal Nexus operations (SCHEDULED→STARTED→terminal, three-tier timeouts).
type CommandStatus uint8

const (
	// CommandPending: enqueued, awaiting send to device transport.
	//
	// IMPORTANT: iota+1 ensures the zero value (0) is NOT a valid status.
	// A forgotten/uninitialised CommandEntry.Status will not silently appear valid.
	CommandPending   CommandStatus = iota + 1 // = 1
	CommandSent                               // transmitted to device transport
	CommandDelivered                          // device ACK'd receipt (not execution)
	CommandSucceeded                          // device confirmed successful execution
	CommandFailed                             // permanent failure (device error or retries exhausted)
	CommandExpired                            // deadline elapsed before completion
	CommandCanceled                           // explicitly canceled by operator/system
)

// Valid reports whether s is a recognised CommandStatus value.
func (s CommandStatus) Valid() bool {
	return s >= CommandPending && s <= CommandCanceled
}

// String returns a human-readable label for the CommandStatus.
func (s CommandStatus) String() string {
	switch s {
	case 0:
		return "invalid"
	case CommandPending:
		return "pending"
	case CommandSent:
		return "sent"
	case CommandDelivered:
		return "delivered"
	case CommandSucceeded:
		return "succeeded"
	case CommandFailed:
		return "failed"
	case CommandExpired:
		return "expired"
	case CommandCanceled:
		return "canceled"
	default:
		return fmt.Sprintf("command_status(%d)", s)
	}
}

// IsTerminal reports whether s is a terminal (final) state.
// Terminal states: Succeeded, Failed, Expired, Canceled.
func (s CommandStatus) IsTerminal() bool {
	switch s {
	case CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Transition table
// ---------------------------------------------------------------------------

// commandTransitions maps each non-terminal state to its valid target states.
var commandTransitions = map[CommandStatus][]CommandStatus{
	CommandPending:   {CommandSent, CommandExpired, CommandCanceled},
	CommandSent:      {CommandDelivered, CommandFailed, CommandExpired, CommandCanceled},
	CommandDelivered: {CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled},
	// Terminal states have no outgoing transitions.
}

// CanTransitionTo reports whether s can transition to target.
func (s CommandStatus) CanTransitionTo(target CommandStatus) bool {
	return slices.Contains(commandTransitions[s], target)
}

// ValidTransitions returns the set of states reachable from s.
// Returns nil for terminal states.
func (s CommandStatus) ValidTransitions() []CommandStatus {
	targets := commandTransitions[s]
	if len(targets) == 0 {
		return nil
	}
	out := make([]CommandStatus, len(targets))
	copy(out, targets)
	return out
}

// Transition validates a state transition from → to and returns an error
// if the transition is not allowed. This is a pure validation function;
// it does NOT mutate any state.
func Transition(from, to CommandStatus) error {
	if from.CanTransitionTo(to) {
		return nil
	}
	return errcode.New(errcode.ErrValidationFailed,
		fmt.Sprintf("outbox: invalid command transition %s -> %s", from, to))
}

// ---------------------------------------------------------------------------
// CommandTimeouts — three-tier timeout configuration
// ---------------------------------------------------------------------------

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

// CommandTimeouts configures per-phase deadlines for an L4 command.
// Zero duration means no timeout for that phase.
//
// Adapter responsibility: the kernel defines timeout configuration and
// pure deadline calculation (via CommandEntry.DeadlineFor). The adapter
// MUST run a periodic sweeper (e.g., every 30s–60s) that queries
// non-terminal commands, computes DeadlineFor for each active phase,
// and calls AdvanceStatus(..., CommandExpired, now) when now exceeds
// the deadline.
//
// ref: Temporal Nexus operations — ScheduleToCloseTimeout, ScheduleToStartTimeout,
// StartToCloseTimeout. GoCell simplifies to three tiers.
type CommandTimeouts struct {
	// ScheduleToSend: max wait from creation to device transport delivery.
	ScheduleToSend time.Duration
	// SendToComplete: max wait from Sent to terminal state (Succeeded/Failed).
	SendToComplete time.Duration
	// OverallDeadline: absolute max from creation to any terminal state.
	OverallDeadline time.Duration
}

// ---------------------------------------------------------------------------
// CommandEntry — L4 command record
// ---------------------------------------------------------------------------

// CommandEntry represents a single L4 command enqueued for device execution.
//
// The lifecycle: Pending → Sent → Delivered → Succeeded/Failed/Expired/Canceled.
// Adapters persist and advance the status via CommandStateAdvancer.
type CommandEntry struct {
	ID          string
	DeviceID    string
	CommandType string            // e.g., "reboot", "cert-renew", "config-push"
	Payload     []byte
	Status      CommandStatus
	Metadata    map[string]string // extensible key-value pairs

	Timeouts CommandTimeouts

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
func (e *CommandEntry) DeadlineFor(phase TimeoutPhase) time.Time {
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
// Creation-time invariants (enforced by NewCommandEntry):
//   - Status must be CommandPending
//   - SentAt, DeliveredAt, CompletedAt must be nil
//   - Attempt must be 0
//
// These constraints ensure that callers cannot bypass the state machine
// by constructing a CommandEntry with an arbitrary status or timestamps.
func (e *CommandEntry) ValidateNew() error {
	if e.ID == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry missing ID")
	}
	if e.DeviceID == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry missing DeviceID")
	}
	if e.CommandType == "" {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry missing CommandType")
	}
	if len(e.Payload) == 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry missing Payload")
	}
	if !e.Status.Valid() {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry has invalid Status")
	}
	if e.Status != CommandPending {
		return errcode.New(errcode.ErrValidationFailed, "outbox: new command entry must have Pending status (use AdvanceCommand to change status)")
	}
	if e.CreatedAt.IsZero() {
		return errcode.New(errcode.ErrValidationFailed, "outbox: command entry missing CreatedAt")
	}
	if e.SentAt != nil || e.DeliveredAt != nil || e.CompletedAt != nil {
		return errcode.New(errcode.ErrValidationFailed, "outbox: new command entry must not have phase timestamps (SentAt/DeliveredAt/CompletedAt)")
	}
	if e.Attempt != 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: new command entry must have Attempt=0")
	}
	if e.Timeouts.ScheduleToSend < 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: ScheduleToSend timeout must be non-negative")
	}
	if e.Timeouts.SendToComplete < 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: SendToComplete timeout must be non-negative")
	}
	if e.Timeouts.OverallDeadline < 0 {
		return errcode.New(errcode.ErrValidationFailed, "outbox: OverallDeadline timeout must be non-negative")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Constructors — enforce creation-time invariants
// ---------------------------------------------------------------------------

// NewCommandEntry creates a CommandEntry in Pending status with the given
// parameters. This is the only sanctioned way to create a command entry;
// it enforces:
//   - Status = CommandPending (callers cannot create non-Pending commands)
//   - SentAt / DeliveredAt / CompletedAt = nil (no phase timestamps at creation)
//   - Attempt = 0
//   - CreatedAt = now (caller-provided, not wall-clock)
//
// The now parameter makes the constructor pure and deterministically testable.
//
// ref: Temporal StartWorkflowExecution — callers submit intent + timeouts,
// status/timestamps are owned by the server. Time is an explicit event
// parameter, never read from wall clock inside the state machine.
func NewCommandEntry(id, deviceID, commandType string, payload []byte, timeouts CommandTimeouts, now time.Time) CommandEntry {
	return CommandEntry{
		ID:          id,
		DeviceID:    deviceID,
		CommandType: commandType,
		Payload:     payload,
		Status:      CommandPending,
		Timeouts:    timeouts,
		Attempt:     0,
		CreatedAt:   now,
	}
}

// AdvanceCommand validates a transition and applies the timestamp side effects
// that the kernel owns. This is the canonical entry point for all state changes.
// Adapters MUST call this before persisting the new status.
//
// Side effects by target status:
//   - Sent:      sets SentAt, increments Attempt
//   - Delivered: sets DeliveredAt
//   - Succeeded/Failed/Expired/Canceled: sets CompletedAt
//
// Returns an error if the transition is invalid or if a required prerequisite
// timestamp is missing (e.g., transitioning to Delivered without SentAt).
func AdvanceCommand(entry *CommandEntry, to CommandStatus, now time.Time) error {
	if err := Transition(entry.Status, to); err != nil {
		return err
	}

	switch to {
	case CommandSent:
		entry.SentAt = &now
		entry.Attempt++
	case CommandDelivered:
		if entry.SentAt == nil {
			return errcode.New(errcode.ErrValidationFailed,
				"outbox: cannot transition to Delivered without SentAt")
		}
		entry.DeliveredAt = &now
	case CommandSucceeded, CommandFailed, CommandExpired, CommandCanceled:
		entry.CompletedAt = &now
	}

	entry.Status = to
	return nil
}

// ResetForRetry resets a command back to Pending for retry. This is the only
// sanctioned way to retry a command — adapters MUST NOT directly mutate
// Status, SentAt, or other fields to simulate retry.
//
// Allowed source states:
//   - CommandSent:   transport delivery failed, retry from scratch
//   - CommandFailed: explicit operator/system retry of a failed command
//
// Disallowed source states:
//   - CommandPending:   already pending (caller bug, no-op not allowed)
//   - CommandDelivered: device ACK'd receipt, resending would duplicate execution
//   - CommandSucceeded/CommandExpired/CommandCanceled: semantically final
//
// Side effects:
//   - Status → CommandPending
//   - SentAt, DeliveredAt, CompletedAt → nil
//   - Attempt is preserved (tracks total attempts across retries)
//   - All other fields (ID, DeviceID, Payload, Timeouts, Metadata, CreatedAt) preserved
func ResetForRetry(entry *CommandEntry) error {
	switch entry.Status {
	case CommandSent, CommandFailed:
		// allowed
	default:
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("outbox: cannot reset for retry from status %s (allowed: sent, failed)", entry.Status))
	}

	entry.Status = CommandPending
	entry.SentAt = nil
	entry.DeliveredAt = nil
	entry.CompletedAt = nil
	return nil
}

// ---------------------------------------------------------------------------
// Adapter injection interfaces
// ---------------------------------------------------------------------------

// CommandWriter persists L4 command entries within a transaction.
type CommandWriter interface {
	// WriteCommand persists a command entry atomically with business state.
	// Consistency: L4 (DeviceLatent).
	WriteCommand(ctx context.Context, entry CommandEntry) error
}

// CommandReader queries L4 command entries.
type CommandReader interface {
	// PendingCommands returns commands in Pending status for the given device,
	// ordered by creation time (FIFO).
	PendingCommands(ctx context.Context, deviceID string) ([]CommandEntry, error)

	// GetCommand returns a single command by ID.
	GetCommand(ctx context.Context, id string) (*CommandEntry, error)
}

// CommandStateAdvancer atomically advances a command's status.
// The adapter MUST call AdvanceCommand with the provided now to compute
// kernel-owned side effects (timestamps, attempt counter) before persisting.
// Implementations SHOULD use optimistic locking (e.g., WHERE status = $from)
// to prevent concurrent transitions.
type CommandStateAdvancer interface {
	// AdvanceStatus atomically transitions a command from one status to another.
	// The now parameter is passed through to AdvanceCommand for timestamp
	// side effects — adapters must not independently decide timestamps.
	// Consistency: L4 (DeviceLatent).
	AdvanceStatus(ctx context.Context, id string, from, to CommandStatus, now time.Time) error
}
