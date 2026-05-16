package errcode

import (
	"context"
	"errors"
	"net"
	"slices"
)

// Category classifies the origin of an error for log-level routing and
// dual-channel triage (infra vs domain). The zero value CategoryUnspecified
// is treated as infra (fail-closed) by all classifiers.
//
// ref: k8s apimachinery pkg/api/errors — IsNotFound dual-channel pattern
// (infra errors must never map to domain not-found).
type Category int

const (
	// CategoryUnspecified is the zero value. Classifiers treat it as infra
	// (fail-closed) to prevent leaking infra faults into domain branches.
	CategoryUnspecified Category = iota

	// CategoryDomain signals a well-known business-layer condition
	// (resource not found, conflict, validation failure).
	CategoryDomain

	// CategoryInfra signals an infrastructure failure (DB down, network
	// timeout, bad connection). Must never be mapped to domain not-found.
	CategoryInfra

	// CategoryValidation signals a caller input validation failure (400-class).
	CategoryValidation

	// CategoryAuth signals an authentication / authorisation failure (401/403).
	CategoryAuth
)

// IsInfraError reports whether err represents an infrastructure failure.
//
// Fail-closed semantics: any error that is not definitively classified as
// a domain / validation / auth error is treated as infra. This prevents
// infra outages from silently propagating into domain-not-found branches.
//
// Returns false only for nil. Returns true for:
//   - context.Canceled / context.DeadlineExceeded
//   - *Error with Category == CategoryInfra or CategoryUnspecified
//   - any unrecognized plain error (fail-closed)
func IsInfraError(err error) bool {
	if err == nil {
		return false
	}

	// Well-known infra sentinels.
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Classified errcode: only domain / validation / auth are NOT infra.
	var ec *Error
	if errors.As(err, &ec) {
		switch ec.Category {
		case CategoryDomain, CategoryValidation, CategoryAuth:
			return false
		default:
			// CategoryInfra or CategoryUnspecified → fail-closed infra.
			return true
		}
	}

	// Unrecognized plain error → fail-closed, treat as infra.
	return true
}

// IsDomainNotFound reports whether err is a domain-layer not-found condition
// whose code is in the caller-supplied whitelist. Both conditions must hold:
//
//  1. err must be an *Error with Category == CategoryDomain
//  2. err.Code must appear in codes
//
// This two-gated check prevents infra errors from ever matching, regardless
// of which code they carry — the dual-channel invariant from k8s IsNotFound.
//
// Callers pass Code constants directly; no string(...) conversion is needed.
func IsDomainNotFound(err error, codes ...Code) bool {
	if err == nil {
		return false
	}
	var ec *Error
	if !errors.As(err, &ec) {
		return false
	}
	if ec.Category != CategoryDomain {
		return false
	}
	return slices.Contains(codes, ec.Code)
}

// WrapInfra is the single typed funnel for producing a transient (retry-safe)
// infrastructure error. It is the ONLY constructor that sets the private
// Error.transient marker — adapter classifiers (classifyPGError /
// classifyRedisError / classifyS3Error) route their transient branch through
// it so that IsTransient can recognize the result.
//
// Behavior:
//   - Kind = KindUnavailable (HTTP 503 "retry after a brief delay")
//   - Category = CategoryInfra
//   - Code = the caller's operation code (e.g. ERR_ADAPTER_PG_QUERY) — there
//     is no parallel ErrAdapter*Transient code set; transient-vs-permanent is
//     the single Kind+marker axis, queryable in metrics by Kind.
//   - the private transient marker is set true
//
// The const-literal restriction documented on New applies to message.
//
// Funnel double-lock (per .claude/rules/gocell/ai-collab.md "Funnel 双向锁"):
//   - upstream Hard: a transient adapter error is producible only via this
//     function; the marker field is unexported so no package outside errcode
//     can set it.
//   - downstream Hard: IsTransient's *Error positive branch keys only on the
//     marker, never on a code string.
//
// archtest ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 enforces both sides.
//
// ref: jackc/pgx pgconn SafeToRetry; aws/aws-sdk-go-v2 aws/retry RetryableError.
func WrapInfra(code Code, message string, cause error, opts ...Option) *Error {
	e := Wrap(KindUnavailable, code, message, cause, opts...)
	e.Category = CategoryInfra
	e.transient = true
	return e
}

// IsTransient reports whether err is a transient (retry-safe) condition that
// an EventBus handler should Requeue rather than Reject:
//
//	if errcode.IsTransient(err) {
//	    return outbox.Requeue(err)
//	}
//	return outbox.Reject(outbox.NewPermanentError(err))
//
// It returns true when, anywhere in the Unwrap chain:
//   - an *Error carries the WrapInfra transient marker (the single recognized
//     *Error signal — downstream Hard: a transient-looking code constructed
//     via New/Wrap without WrapInfra is NOT transient); or
//   - the error is context.DeadlineExceeded (a deadline may succeed on retry);
//     context.Canceled is deliberately NOT transient (the caller gave up); or
//   - a net.Error reports Timeout()==true (modern replacement for the
//     deprecated net.Error.Temporary(), golang/go #45729); or
//   - an error implements interface{ RetryableError() bool } returning true
//     (the pgconn.SafeToRetry / aws-sdk-go-v2 RetryableError idiom, letting
//     raw SDK errors be recognized before adapter wrapping).
//
// Returns false for nil. Uses errors.As/Is so it traverses chains produced by
// fmt.Errorf("…: %w", err).
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	var ec *Error
	if errors.As(err, &ec) && ec.transient {
		return true
	}

	// context.Canceled is intentionally excluded: a canceled parent context
	// means the work is no longer wanted; retrying is pointless.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var retryable interface{ RetryableError() bool }
	if errors.As(err, &retryable) {
		return retryable.RetryableError()
	}

	return false
}

// IsExpected4xx reports whether err maps to an HTTP 400-499 response code.
// These are expected client-side / business rejection conditions that callers
// should log at Warn level; true infrastructure failures should be Error.
//
// Returns false for nil and for unclassified / plain errors (which are infra).
func IsExpected4xx(err error) bool {
	if err == nil {
		return false
	}
	var ec *Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Kind.IsClient()
}
