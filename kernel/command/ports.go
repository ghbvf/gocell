package command

import (
	"context"
	"time"
)

// Writer persists L4 command entries within a transaction.
type Writer interface {
	// WriteCommand persists a command entry atomically with business state.
	// Consistency: L4 (DeviceLatent).
	WriteCommand(ctx context.Context, entry Entry) error
}

// Reader queries L4 command entries.
type Reader interface {
	// PendingCommands returns commands in Pending status for the given device,
	// ordered by creation time (FIFO).
	PendingCommands(ctx context.Context, deviceID string) ([]Entry, error)

	// GetCommand returns a single command by ID.
	GetCommand(ctx context.Context, id string) (*Entry, error)
}

// StateAdvancer atomically advances a command's status.
// The adapter MUST call AdvanceCommand with the provided now to compute
// kernel-owned side effects (timestamps, attempt counter) before persisting.
// Implementations SHOULD use optimistic locking (e.g., WHERE status = $from)
// to prevent concurrent transitions.
//
// Callers that need to chain multiple AdvanceStatus calls atomically (e.g.,
// Pending→Sent→Delivered→Succeeded in a single HTTP request) SHOULD wrap them
// in a transaction at the adapter level. The kernel interface does not expose
// transactional scope; this is intentional to keep it pluggable (in-memory
// adapters can satisfy it trivially).
//
// Typical adapter implementation:
//
//	func (a *PGAdapter) AdvanceStatus(ctx context.Context, id string, from, to Status, now time.Time) error {
//	    cmd, err := a.GetCommand(ctx, id)
//	    if err != nil { return err }
//	    if err := AdvanceCommand(cmd, to, now); err != nil { return err }
//	    // Optimistic lock: WHERE status = from prevents concurrent transitions.
//	    return a.updateStatus(ctx, cmd) // persist cmd with updated timestamps
//	}
type StateAdvancer interface {
	// AdvanceStatus atomically transitions a command from one status to another.
	// The now parameter is passed through to AdvanceCommand for timestamp
	// side effects — adapters must not independently decide timestamps.
	// Consistency: L4 (DeviceLatent).
	AdvanceStatus(ctx context.Context, id string, from, to Status, now time.Time) error
}
