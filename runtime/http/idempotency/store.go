// Package idempotency provides HTTP-layer idempotency primitives for GoCell.
//
// It is a minimal specialization of kernel/idempotency tailored to the HTTP
// request/response lifecycle: ClaimDone carries a replayed response blob
// (RecordedResponse) rather than a bare "already processed" signal, and the
// HTTP-specific Receipt.Record method commits the response alongside the done
// state.
//
// Relation to kernel/idempotency:
//   - ClaimState (ClaimAcquired / ClaimDone / ClaimBusy) is REUSED from
//     kernel/idempotency. Do NOT clone the enum.
//   - DefaultTTL (24h) and DefaultLeaseTTL (5min) constants come from
//     kernel/idempotency.
//   - This is a specialization, NOT a parallel abstraction.
package idempotency

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
)

// Store is the HTTP-layer idempotency backend. Implementations live in
// adapters/ (e.g., adapters/redis).
//
// Claim semantics:
//   - (ClaimAcquired, nil, receipt, nil) — caller MUST run the HTTP handler,
//     then call receipt.Record with the response, then receipt.Release on error.
//   - (ClaimDone, rec, noopReceipt, nil) — a previous request already completed;
//     caller MUST replay rec directly and MUST NOT run the handler again.
//   - (ClaimBusy, nil, noopReceipt, nil) — another request is currently
//     in-flight for the same key; caller SHOULD return 409 ErrIdempotencyInProgress.
//   - (_, _, _, err) — infrastructure error; caller MUST fail closed (do not run handler).
//
// ns is a namespace prefix that scopes keys across different parts of the
// application (e.g., cell ID or route group prefix). key is the idempotency
// key from the request header.
type Store interface {
	Claim(ctx context.Context, ns, key string, leaseTTL time.Duration) (idempotency.ClaimState, *RecordedResponse, Receipt, error)
}

// Receipt is the HTTP-specific lifecycle handle for a single acquired
// idempotency lease. It mirrors kernel/idempotency.Receipt but Commit carries
// the response blob — the HTTP layer must persist the response for future
// replay before committing.
//
// Usage for ClaimAcquired:
//  1. Run the HTTP handler to produce a response.
//  2. Call Record(ctx, resp, doneTTL) to persist the response and commit the lease.
//  3. On handler error (before or after partial writes), call Release(ctx)
//     so the lease expires and another request may retry.
//
// Record and Release are idempotent with respect to the lease state: calling
// them on a non-acquired claim state returns kernel/idempotency.ErrNoClaimLease.
type Receipt interface {
	// Record persists resp as the canonical response for this idempotency key
	// and commits the lease, making the key's state ClaimDone for doneTTL.
	// doneTTL controls how long future requests replay the stored response
	// (typically kernel/idempotency.DefaultTTL = 24h).
	Record(ctx context.Context, resp *RecordedResponse, doneTTL time.Duration) error

	// Release abandons the in-progress lease without recording a response,
	// allowing the key to be re-claimed by a subsequent request.
	Release(ctx context.Context) error
}
