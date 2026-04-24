package command

import (
	"context"
	"fmt"
	"time"
)

// DefaultLeaseDuration is the suggested default lease when callers don't specify
// one in Queue.Dequeue. Intentionally mirrors kernel/idempotency.DefaultLeaseTTL
// (5 minutes) so L4 command leases and L2 consumer leases default to the same
// timeout. Update both if this default changes.
const DefaultLeaseDuration = 5 * time.Minute

// AuthzFunc is a caller-supplied permission hook invoked at the Enqueue boundary.
// Pass nil to skip (demo/test mode). See docs for T3 DEVICE-ENQUEUE-RBAC.
type AuthzFunc func(ctx context.Context) error

// AckReason classifies how a command ended.
type AckReason uint8

const (
	AckSuccess  AckReason = iota + 1 // device executed successfully; → StatusSucceeded
	AckFailed                        // device reported permanent failure; → StatusFailed
	AckTimeout                       // caller timed out waiting; releases lease for retry
	AckRejected                      // operator/system cancel; → StatusCanceled
)

// Valid reports whether r is a recognised AckReason value.
func (r AckReason) Valid() bool {
	return r >= AckSuccess && r <= AckRejected
}

// String returns a human-readable label for the AckReason.
func (r AckReason) String() string {
	switch r {
	case 0:
		return "invalid"
	case AckSuccess:
		return "success"
	case AckFailed:
		return "failed"
	case AckTimeout:
		return "timeout"
	case AckRejected:
		return "rejected"
	default:
		return fmt.Sprintf("ack_reason(%d)", r)
	}
}

// EnqueueOptions configures a single Enqueue call.
// Lease duration is not set at enqueue time — it is determined by the
// leaseDuration parameter of Queue.Dequeue. DefaultLeaseDuration is the
// recommended default for Dequeue callers.
type EnqueueOptions struct {
	// IdempotencyKey dedups retried Enqueue calls; empty = no dedup guarantee.
	IdempotencyKey string
	// Authz is invoked before any write; return non-nil to reject. Use nil to skip.
	Authz AuthzFunc
}

// Queue is the kernel-level L4 command queue facade. Implementations live in
// adapters/postgres or in-memory (commandtest package).
//
// Enqueue stores an Entry atomically. Dequeue returns up to n Pending entries
// for targetID with their lease set. Ack finalises; ExtendLease renews; Cancel
// aborts a non-terminal command.
type Queue interface {
	Enqueue(ctx context.Context, entry Entry, opts EnqueueOptions) error
	Dequeue(ctx context.Context, targetID string, n int, leaseDuration time.Duration) ([]Entry, error)
	Ack(ctx context.Context, commandID string, reason AckReason, now time.Time) error
	ExtendLease(ctx context.Context, commandID string, extension time.Duration, now time.Time) error
	Cancel(ctx context.Context, commandID string, now time.Time) error
	ListPending(ctx context.Context, targetID string, limit int) ([]Entry, error)
}
