package command

import (
	"context"
)

// Writer persists L4 command entries within a transaction. Primarily used by
// adapter internals and test fixtures to seed entries bypassing Enqueue
// validation; service/application code MUST use Queue.Enqueue instead.
type Writer interface {
	// WriteCommand persists a command entry atomically with business state.
	// Consistency: L4 (DeviceLatent).
	WriteCommand(ctx context.Context, entry Entry) error
}

// ScanFilter narrows an ActiveScanner.ScanActive call. Empty fields mean
// "no filter" (all non-terminal entries across all devices).
type ScanFilter struct {
	// DeviceID, if non-empty, restricts results to one device. Empty = all devices.
	DeviceID string
	// Statuses, if non-empty, restricts results to the listed statuses.
	// Empty slice = all non-terminal statuses (Pending / Sent / Delivered).
	// Terminal statuses in Statuses are silently ignored — scanners return
	// only non-terminal entries.
	Statuses []Status
}

// ActiveScanner queries all non-terminal command entries matching a filter.
// This is the role-based scan port used by Sweeper (timeout detection) and
// operational views (list pending/in-flight commands). It is deliberately
// distinct from Queue.Dequeue (the claim-with-lease primary consumer path):
// scanners do not advance state or issue leases, and scanners are allowed to
// see Sent/Delivered entries that Dequeue must hide.
//
// ref: Temporal HistoryService active-timer scan + JetStream ConsumerInfo —
// separate from the primary Fetch/dispatch path; read-only lens over
// non-terminal entries.
type ActiveScanner interface {
	// ScanActive returns non-terminal entries matching filter, ordered by
	// CreatedAt ascending (FIFO). Implementations MAY impose an adapter-level
	// result cap but MUST document it.
	ScanActive(ctx context.Context, filter ScanFilter) ([]Entry, error)
	// GetCommand returns a single command by ID. Returns (nil, nil) when not found.
	GetCommand(ctx context.Context, id string) (*Entry, error)
}
