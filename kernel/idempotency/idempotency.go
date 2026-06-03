// Package idempotency defines the consumer-side idempotency interface.
// Implementations live in adapters/ (e.g., adapters/redis).
package idempotency

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ErrLeaseExpired indicates the processing lease is no longer held —
// either it expired naturally or another consumer claimed it.
// Callers MUST stop business logic on this error and proceed to Release.
var ErrLeaseExpired = errcode.New(errcode.KindConflict, errcode.ErrIdempotencyLeaseExpired, "idempotency: processing lease expired")

// ErrNoClaimLease indicates Receipt methods were called for a Claim result
// that did not acquire a processing lease.
var ErrNoClaimLease = errcode.New(errcode.KindConflict, errcode.ErrIdempotencyNoClaimLease, "idempotency: no acquired claim lease")

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

// NonAcquiredReceipt returns a sentinel receipt for ClaimDone and ClaimBusy.
// Callers must branch on ClaimState and ignore this receipt unless the state is
// ClaimAcquired; if misused, its methods return ErrNoClaimLease.
func NonAcquiredReceipt() Receipt {
	return nonAcquiredReceipt{}
}

type nonAcquiredReceipt struct{}

func (nonAcquiredReceipt) Commit(context.Context) error {
	return ErrNoClaimLease
}

func (nonAcquiredReceipt) Release(context.Context) error {
	return ErrNoClaimLease
}

func (nonAcquiredReceipt) Extend(context.Context, time.Duration) error {
	return ErrNoClaimLease
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

// ClaimerKind classifies a Claimer implementation for startup validation.
// It mirrors kernel/auth.NonceStoreKind: the implementation self-reports its
// kind so composition-root control-plane validation can reject a single-process
// claimer in a multi-pod deployment.
//
// Strength of the mechanism (AI-robust: Medium, not Hard). Binding the kind to a
// method on the implementation removes the caller-supplied-label attack: a
// consumer cannot fill a free-standing "kind" field that contradicts the claimer
// it actually wired, because the kind travels with the value. It does NOT make
// the kind unforgeable — Kind() is an ordinary method, so a buggy or hostile
// implementation can still return the wrong value (the fakes in
// runtime/composition/shared_deps_test.go do exactly that on purpose to isolate
// validator branches). The residual "an implementation lies about its own kind"
// risk is closed not by the type system but by (a) the production
// implementations being a fixed, in-repo set and (b) a per-implementation
// return-value assertion on each (kernel/idempotency
// TestInMemClaimer_Kind_ReportsInMemory, adapters/redis
// TestIdempotencyClaimer_Kind_ReportsDistributed). Validators MUST therefore
// fail-closed on any unrecognized kind rather than wave it through a permissive
// default (see runtime/composition validateProductionNonceStore).
//
// Note the deliberate absence of a noop variant (unlike kernel/auth.NonceStoreKind):
// every Claimer coordinates idempotency. The absence of coordination is expressed
// by not wiring a Claimer at all, not by a noop implementation.
type ClaimerKind string

const (
	// ClaimerKindInMemory is the single-process map-backed implementation.
	// It does NOT coordinate idempotency across replicas.
	ClaimerKindInMemory ClaimerKind = "in_memory"
	// ClaimerKindDistributed is a shared backend (Redis, etc.) safe for
	// multi-pod deployments.
	ClaimerKindDistributed ClaimerKind = "distributed"
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
	//   - (ClaimDone, NonAcquiredReceipt(), nil) — already processed; caller should Ack.
	//   - (ClaimBusy, NonAcquiredReceipt(), nil) — another consumer is processing; caller should Requeue.
	//   - (_, nil, err) — infrastructure error.
	Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, Receipt, error)

	// Kind reports the implementation classification (in-memory vs distributed)
	// for composition-root control-plane validation.
	Kind() ClaimerKind
}
