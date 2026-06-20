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
	// IdempotencyKey enforces STATE-AWARE ACTIVE uniqueness: at most one command
	// with this key may be NON-terminal (Pending/Sent/Delivered) at a time. A
	// re-enqueue under the same key is coalesced (no-op, nil error) while a holder
	// is non-terminal, and is ADMITTED once the prior holder reaches a terminal
	// status (Succeeded/Failed/Expired/Canceled) — so a later retry of the same
	// logical command enqueues a fresh entry. Empty = no uniqueness.
	//
	// This is the River UniqueOpts.ByState / Temporal single-open-execution model:
	// the queue (the owner of active-command state) is the correctness authority
	// for "at most one in-flight per identity"; producers MUST NOT reconstruct it
	// from side state. Uniqueness is keyed on Status, so terminal transitions
	// (Ack/Cancel/Sweeper-AckTimeout) release the key automatically — there is no
	// separate release call to forget. PG enforces it with a partial unique index
	// `WHERE … AND status IN (Pending,Sent,Delivered)`; the in-mem store derives it
	// from Status. Both are pinned by the commandtest conformance suite
	// (Enqueue/ActiveKeyBlocksAcrossNonTerminal + Enqueue/KeyReleasedOn*).
	//
	// CAUTION: a command that never reaches terminal would hold its key forever and
	// block all retries — so a producer requesting active-uniqueness MUST also give
	// the command a terminal-guaranteeing deadline (see runtime/command
	// WithActiveUniqueness, which couples the two so the unsafe combination is
	// inexpressible).
	IdempotencyKey string
	// Authz is invoked before any write; return non-nil to reject. Use nil to skip.
	Authz AuthzFunc
	// MaxPendingPerDevice caps how many Pending (status=1) commands a single
	// device may hold. When > 0, Enqueue atomically rejects with
	// KindRateLimited/ErrRateLimited if admitting this command would exceed the
	// cap — the count and the insert run under one lock/transaction, so the cap is
	// a HARD per-device invariant under concurrency and across instances (no
	// read-then-write TOCTOU). 0 = uncapped.
	//
	// Same authority model as IdempotencyKey: the queue OWNS active-command state,
	// so it is the correctness authority for "at most N Pending per device";
	// producers MUST NOT reconstruct the cap from a separate ScanActive+check (that
	// reintroduces the very read-then-write race this field exists to close). Only
	// Pending counts — in-flight Sent/Delivered resolve on their own and free a
	// slot. A re-enqueue coalesced by IdempotencyKey adds nothing and never trips
	// the cap. Pinned by the commandtest conformance suite
	// (Enqueue/MaxPendingPerDeviceCap) across the in-mem and PG implementations.
	MaxPendingPerDevice int
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
