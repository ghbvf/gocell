package errcode

import (
	"context"
	"errors"
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

// IsTransient reports whether err, or any error in its Unwrap chain, carries
// the ErrKeyProviderTransient code. It is the canonical predicate for routing
// KeyProvider failures in EventBus handlers:
//
//	if errcode.IsTransient(err) {
//	    return outbox.Requeue(err)
//	}
//	return outbox.Reject(err)
//
// Transient conditions map to Vault HTTP 503 / 429 / 408 / 499 (sealed,
// rate-limited, request timeout). All other KeyProvider errors are permanent
// (400 / 403 / 404) and must be routed to DispositionReject → DLX.
//
// Returns false for nil. Uses errors.As so it correctly traverses chains
// produced by fmt.Errorf("…: %w", err).
//
// ref: aws/aws-encryption-sdk-python src/aws_encryption_sdk/exceptions.py
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var ec *Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Code == ErrKeyProviderTransient
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
