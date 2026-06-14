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

	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ErrFingerprintMismatch is returned by Store.Claim when the same
// Idempotency-Key is presented again with a different request body fingerprint.
// The middleware converts this sentinel into a 422 (KindUnprocessable) response
// with code ErrIdempotencyKeyReused, optionally enriched with the names of the
// top-level request fields that differ (per-field diff).
//
// Implementations MUST return a *FingerprintMismatchError (which wraps this
// sentinel) so callers can use errors.Is for detection AND errors.As to recover
// the stored fingerprint blob for diffing.
var ErrFingerprintMismatch = errcode.New(
	errcode.KindUnprocessable,
	errcode.ErrIdempotencyKeyReused,
	// msgFingerprintMismatch must be a const literal per MESSAGE-CONST-LITERAL-01.
	"idempotency key reused with a different request body",
)

// FingerprintMismatchError wraps ErrFingerprintMismatch and carries the stored
// canonical fingerprint blob recorded for the original request, so the
// middleware can compute a per-field diff naming the top-level fields that
// changed. Stored holds ONLY per-field hashes (hex sha256), never raw request
// values — echoing a stored request value is structurally impossible because raw
// values are never persisted.
type FingerprintMismatchError struct {
	// Stored is the canonical fingerprint blob (see computeFingerprint) recorded
	// for the original Claim. Opaque to the Store; parsed only by the middleware.
	Stored string
}

func (e *FingerprintMismatchError) Error() string { return ErrFingerprintMismatch.Error() }

// Unwrap returns the ErrFingerprintMismatch sentinel so errors.Is keeps working.
func (e *FingerprintMismatchError) Unwrap() error { return ErrFingerprintMismatch }

// errInProgress is the 409 ClaimBusy sentinel (an in-flight lease exists for the
// key). It is the single source for the 409 leg of FrameworkStatuses and is the
// error the middleware busy branch writes verbatim.
var errInProgress = errcode.New(
	errcode.KindConflict,
	errcode.ErrIdempotencyInProgress,
	msgInProgress,
)

// FrameworkStatuses returns the client-facing HTTP status codes the idempotency
// middleware injects on a mutating, non-exempt route: 409 (ClaimBusy, in-flight
// key) and 422 (key reused with a different request body). Both are derived from
// the actual sentinels the middleware emits, so this set cannot drift from
// runtime behavior.
//
// It is the single source the kernel/metadata oracle
// HTTPTransportMeta.IdempotencyFrameworkStatuses() is bound to by archtest
// IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01. The kernel oracle re-declares the
// set as an integer literal because kernel/ must not import runtime/ (layering);
// the archtest forbids the two from diverging.
func FrameworkStatuses() []int {
	return []int{
		errInProgress.Status(),          // 409 — ClaimBusy (in-flight key)
		ErrFingerprintMismatch.Status(), // 422 — key reused with a different body
	}
}

// Store is the HTTP-layer idempotency backend. Implementations live in
// adapters/ (e.g., adapters/redis).
//
// Claim semantics — discriminate on the returned ClaimState:
//
//	switch state {
//	case idempotency.ClaimAcquired:
//	    // rec is nil; receipt is the acquired lease.
//	    // Run the handler, call receipt.Record on success, receipt.Release on error.
//	case idempotency.ClaimDone:
//	    // rec is non-nil; replay it. MUST NOT run the handler again.
//	case idempotency.ClaimBusy:
//	    // rec is nil; another request is in-flight. Return 409.
//	}
//
// On error (err != nil), fail closed: do not run the handler.
// If err wraps ErrFingerprintMismatch (errors.Is), the same key was previously
// claimed with a different fingerprint — the middleware returns 422
// (KindUnprocessable) with ErrIdempotencyKeyReused. Do NOT add a 4th ClaimState
// for this case; use the sentinel error path instead. Implementations MUST
// return a *FingerprintMismatchError (which wraps the sentinel) so the middleware
// can recover the stored fingerprint blob for the per-field diff.
//
// k is the sealed (namespace, key) pair produced by a sanctioned constructor
// (DeriveKey from the HTTP request + principal isolation tuple, or DeriveCommandKey
// from the command isolation tuple); the Store reads it via k.Namespace() (the
// tenant scope, or "_notenant" when absent) and k.Key() (the per-request key — see
// those constructors for the byte layout). A raw (ns,key) string pair cannot reach Claim —
// that typed boundary is the downstream Hard gate of the node-agnostic invariant
// (#1449/#1610). fingerprint is the opaque canonical blob produced by
// computeFingerprint (JSON whose Body field is hex(sha256(rawBody)) and whose
// Fields field maps top-level field names to per-field hashes for the diff); the
// Store treats it as an opaque string and only compares / round-trips it.
type Store interface {
	Claim(ctx context.Context, k IdempotencyKey, fingerprint string, leaseTTL time.Duration) (idempotency.ClaimState, *RecordedResponse, Receipt, error) //nolint:lll // full Claim contract signature cannot be wrapped in an interface decl
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
//
// Store implementors: both Record and Release are typically called from a
// defer after the request context may already be canceled (timeout/client
// disconnect). Use context.WithoutCancel(ctx) when issuing the underlying
// store operations so the commit/release reaches the backend even when the
// request context is canceled. See runtime/http/idempotency.Middleware for
// the reference implementation.
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
