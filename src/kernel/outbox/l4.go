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

	Attempt     int        // current attempt number (0 = first attempt)
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

// Validate checks that required fields are present, status is valid, and
// timeouts are non-negative.
func (e CommandEntry) Validate() error {
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
// Adapter injection interfaces
// ---------------------------------------------------------------------------

// CommandWriter persists L4 command entries within a transaction.
type CommandWriter interface {
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
// The adapter MUST use the Transition function to validate the transition
// before persisting. Implementations SHOULD use optimistic locking
// (e.g., WHERE status = $from) to prevent concurrent transitions.
type CommandStateAdvancer interface {
	AdvanceStatus(ctx context.Context, id string, from, to CommandStatus) error
}
