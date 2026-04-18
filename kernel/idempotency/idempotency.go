// Package idempotency defines the consumer-side idempotency interface.
// Implementations live in adapters/ (e.g., adapters/redis).
package idempotency

import (
	"context"
	"errors"
	"time"
)

// ErrLeaseExpired indicates the processing lease is no longer held —
// either it expired naturally or another consumer claimed it.
// Callers MUST stop business logic on this error and proceed to Release.
var ErrLeaseExpired = errors.New("idempotency: processing lease expired")

// DefaultTTL is the standard idempotency key TTL per the EventBus specification.
const DefaultTTL = 24 * time.Hour

// DefaultLeaseTTL is the default processing-lease TTL.
// If a consumer crashes mid-processing, the lease expires after this duration,
// allowing another consumer to re-claim the message.
const DefaultLeaseTTL = 5 * time.Minute

// Receipt represents the lifecycle handle for a single acquired idempotency
// lease. It is canonical to the consumer-side idempotency flow:
// Claim acquires a lease, then the caller Commits it after broker Ack or
// Releases it after Reject/Requeue.
//
// Disposition → Receipt mapping (broker-layer convention):
//   - DispositionAck    + broker Ack success  → Receipt.Commit()
//   - DispositionReject + broker Nack success → Receipt.Release() (allows DLQ replay)
//   - DispositionRequeue + broker Nack success → Receipt.Release()
//   - Any broker Ack/Nack failure             → Receipt.Release()
//
// For long-running handlers, callers MAY call Extend periodically to prevent
// the lease from expiring mid-processing.
//
// Callers MUST use context.WithoutCancel for Receipt operations to ensure the
// idempotency state is persisted even during graceful shutdown.
type Receipt interface {
	Commit(ctx context.Context) error
	Release(ctx context.Context) error

	// Extend resets the processing-lease TTL to the given duration from now.
	// Returns ErrLeaseExpired if the lease is no longer held (fencing failure)
	// or wraps the underlying backend error otherwise.
	Extend(ctx context.Context, ttl time.Duration) error
}

// ---------------------------------------------------------------------------
// ClaimState — two-phase idempotency model (Solution B)
// ---------------------------------------------------------------------------

// ClaimState is the result of a Claim attempt.
type ClaimState uint8

const (
	// ClaimAcquired means the caller obtained the processing lease and should
	// execute business logic. The returned Receipt MUST be Committed on success
	// or Released on failure/requeue.
	ClaimAcquired ClaimState = iota

	// ClaimDone means a previous consumer already completed processing.
	// The caller should Ack without running business logic.
	ClaimDone

	// ClaimBusy means another consumer currently holds the processing lease.
	// The caller should Requeue so the broker redelivers later.
	ClaimBusy
)

// Claimer provides two-phase idempotency for event consumers (Solution B).
//
// Flow:
//  1. Claim(key) → ClaimAcquired + Receipt
//  2. Execute business logic
//     3a. Success → broker Ack → receipt.Commit()
//     3b. Transient failure → broker Nack(requeue) → receipt.Release()
//     3c. Permanent failure → broker Nack(no-requeue) → receipt.Release()
//
// Note: Reject (3c) uses Release, not Commit, so that messages replayed
// from a dead-letter queue can be reprocessed after the root cause is fixed.
//
// This eliminates the race condition where TryProcess marks a key as done
// before the broker has acknowledged the message.
type Claimer interface {
	// Claim attempts to acquire a processing lease for the given key.
	//
	// Returns:
	//   - (ClaimAcquired, receipt, nil) — caller should process, then Commit or Release.
	//   - (ClaimDone, nil, nil) — already processed; caller should Ack.
	//   - (ClaimBusy, nil, nil) — another consumer is processing; caller should Requeue.
	//   - (_, nil, err) — infrastructure error.
	Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, Receipt, error)
}
