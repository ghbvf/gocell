// Package idempotency defines the consumer-side idempotency interface.
// Implementations live in adapters/ (e.g., adapters/redis).
package idempotency

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// DefaultTTL is the standard idempotency key TTL per the EventBus specification.
const DefaultTTL = 24 * time.Hour

// DefaultLeaseTTL is the default processing-lease TTL.
// If a consumer crashes mid-processing, the lease expires after this duration,
// allowing another consumer to re-claim the message.
const DefaultLeaseTTL = 5 * time.Minute

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
//  3a. Success → broker Ack → receipt.Commit()
//  3b. Transient failure → broker Nack(requeue) → receipt.Release()
//  3c. Permanent failure → broker Nack(no-requeue) → receipt.Release()
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
	Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, outbox.Receipt, error)
}

// ---------------------------------------------------------------------------
// Checker — legacy interface (deprecated)
// ---------------------------------------------------------------------------

// Deprecated: Checker is the pre-Solution-B idempotency interface. New code
// should use Claimer which provides two-phase Claim/Commit/Release semantics
// that correctly align idempotency state with broker acknowledgement. (F-ID-01)
//
// Checker will be removed in a future release.
type Checker interface {
	// IsProcessed returns true if the given key has already been processed.
	IsProcessed(ctx context.Context, key string) (bool, error)

	// MarkProcessed marks the key as processed with the given TTL.
	MarkProcessed(ctx context.Context, key string, ttl time.Duration) error

	// TryProcess atomically checks whether key has been processed and marks it if not.
	// Returns true if the caller should process (key was not previously seen).
	// Returns false if already processed (another consumer got there first).
	// This eliminates the TOCTOU race between separate IsProcessed + MarkProcessed calls.
	TryProcess(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// Release removes the idempotency key so that a redelivered message can be
	// processed again. Must be called on requeue/shutdown paths where TryProcess
	// already claimed the key but business logic did not complete.
	Release(ctx context.Context, key string) error
}
