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

// AckReason classifies how a command ended. Every reason maps to a terminal
// status.
//
// ref: JetStream Ack/Nak/Term disposition — only terminal outcomes.
type AckReason uint8

const (
	AckSuccess  AckReason = iota + 1 // device executed successfully → StatusSucceeded
	AckFailed                        // permanent failure → StatusFailed
	AckTimeout                       // deadline elapsed → StatusExpired (used by Sweeper)
	AckRejected                      // device/system rejected the command → StatusCanceled
)

// Valid reports whether r is a recognized AckReason value.
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

// TargetStatus returns the terminal Status produced by this AckReason.
// Panics on invalid reason — callers must guard with Valid() at boundaries.
func (r AckReason) TargetStatus() Status {
	switch r {
	case AckSuccess:
		return StatusSucceeded
	case AckFailed:
		return StatusFailed
	case AckTimeout:
		return StatusExpired
	case AckRejected:
		return StatusCanceled
	default:
		return 0 // invalid; caller must guard
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
// Event-driven state machine: each state transition is triggered by a
// distinct Queue method call, not chained inside Ack:
//
//	Enqueue   → StatusPending (created)
//	Dequeue   → StatusPending → StatusSent        (claim + lease, single atomic step)
//	Report    → StatusSent    → StatusDelivered   (device acknowledged receipt)
//	Ack       → any non-terminal → terminal       (single atomic step)
//	ExtendLease → renew existing lease
//	Cancel    → any non-terminal → StatusCanceled  (operator action)
//
// Ack is single-step atomic: it does NOT chain Pending→Sent→Delivered→terminal.
// Callers skipping Report (e.g., acking directly from StatusSent) produce a
// StatusSent → StatusSucceeded transition with DeliveredAt left nil, indicating
// the device never reported intermediate delivery.
//
// ref: Temporal RecordActivityTaskHeartbeat + RespondActivityTaskCompleted —
// distinct RPCs for in-progress signal vs terminal; state transitions are
// recorded at the event, not batched at ack time.
// ref: JetStream InProgress vs Ack/Nak/Term — distinct client methods for
// in-flight continuation vs disposition.
type Queue interface {
	Enqueue(ctx context.Context, entry Entry, opts EnqueueOptions) error
	Dequeue(ctx context.Context, targetID string, n int, leaseDuration time.Duration) ([]Entry, error)
	Report(ctx context.Context, commandID string, now time.Time) error
	Ack(ctx context.Context, commandID string, reason AckReason, now time.Time) error
	ExtendLease(ctx context.Context, commandID string, extension time.Duration, now time.Time) error
	Cancel(ctx context.Context, commandID string, now time.Time) error
}
