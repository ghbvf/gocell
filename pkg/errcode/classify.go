package errcode

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

// Category classifies the origin of an error for log-level routing and
// dual-channel triage (infra vs domain). The zero value CategoryUnspecified
// is treated as infra (fail-closed) by all classifiers.
//
// ref: k8s apimachinery pkg/api/errors — IsNotFound dual-channel pattern
// (infra errors must never map to domain not-found)
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

// NewInfra creates an *Error with CategoryInfra.
// Use this for storage, network, and dependency failures so they are
// never confused with domain not-found conditions.
func NewInfra(code Code, message string) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryInfra,
	}
}

// NewDomain creates an *Error with CategoryDomain.
// Use this for well-known business-layer conditions (resource missing,
// conflict, etc.) that callers may handle specifically.
func NewDomain(code Code, message string) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryDomain,
	}
}

// WrapInfra creates an *Error with CategoryInfra that wraps the supplied cause.
// Use this when an infrastructure failure has an underlying cause that should be
// preserved for error chain inspection (errors.Is / errors.As / Unwrap).
// The cause is stored in Error.Cause and exposed via Error.Error() in logs.
func WrapInfra(code Code, message string, cause error) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryInfra,
		Cause:    cause,
	}
}

// WrapDomain creates an *Error with CategoryDomain that wraps the supplied cause.
// Use this when a domain-layer condition has an underlying cause to preserve.
// The cause is stored in Error.Cause and exposed via Error.Error() in logs.
func WrapDomain(code Code, message string, cause error) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryDomain,
		Cause:    cause,
	}
}

// NewAuth creates an *Error with CategoryAuth.
// Use this for authentication / authorisation failures (401/403) and attack
// signals such as refresh token reuse detection (OAuth2 RFC 6749 §10.4).
func NewAuth(code Code, message string) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryAuth,
	}
}

// WrapAuth creates an *Error with CategoryAuth that wraps the supplied cause.
// Use this when an authentication / authorisation failure or attack signal
// (e.g. reuse detection per RFC 6749 §10.4) has an underlying cause to
// preserve for error chain inspection (errors.Is / errors.As / Unwrap).
// The cause is stored in Error.Cause and exposed via Error.Error() in logs.
func WrapAuth(code Code, message string, cause error) *Error {
	return &Error{
		Code:     code,
		Message:  message,
		Category: CategoryAuth,
		Cause:    cause,
	}
}

// IsInfraError reports whether err represents an infrastructure failure.
//
// Fail-closed semantics: any error that is not definitively classified as
// a domain / validation / auth error is treated as infra. This prevents
// infra outages from silently propagating into domain-not-found branches.
//
// Returns false only for nil. Returns true for:
//   - context.Canceled / context.DeadlineExceeded
//   - driver.ErrBadConn / sql.ErrConnDone
//   - *Error with Category == CategoryInfra or CategoryUnspecified
//   - any unrecognised plain error (fail-closed)
//
// Stdlib sentinel coverage is intentionally narrow (context.* / sql.Err* /
// driver.ErrBadConn). Adapters that return wrapped plain errors are covered
// by the fail-closed fallback: CategoryUnspecified → treated as infra.
// New adapters that return wrapped custom error types should construct them
// with NewInfra (or WrapInfra) so the category is explicit rather than
// relying on the fallback; no change to classify.go is required.
func IsInfraError(err error) bool {
	if err == nil {
		return false
	}

	// Well-known infra sentinels.
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) {
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

	// Unrecognised plain error → fail-closed, treat as infra.
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
	for _, c := range codes {
		if ec.Code == c {
			return true
		}
	}
	return false
}

// expected4xxCodes is the set of error codes that map to HTTP 400-499 responses.
// These represent expected client-side / business rejection conditions that
// should be logged at Warn level rather than Error.
var expected4xxCodes = map[Code]bool{
	// 400 — bad request / validation
	ErrValidationFailed:          true,
	ErrAuthLoginInvalidInput:     true,
	ErrAuthRefreshInvalidInput:   true,
	ErrAuthSessionInvalidInput:   true,
	ErrAuthIdentityInvalidInput:  true,
	ErrAuthInvalidInput:          true,
	ErrAuthRBACInvalidInput:      true,
	ErrCursorInvalid:             true,
	ErrPageSizeExceeded:          true,
	ErrInvalidTimeFormat:         true,
	ErrConfigInvalidInput:        true,
	ErrConfigPublishInvalidInput: true,
	ErrFlagInvalidInput:          true,
	ErrCheckRefInvalid:           true,
	ErrAuthLogoutInvalidInput:    true,

	// 401 — authentication failure
	ErrAuthUnauthorized:       true,
	ErrAuthTokenInvalid:       true,
	ErrAuthTokenExpired:       true,
	ErrAuthInvalidTokenIntent: true,
	ErrAuthInvalidToken:       true,
	ErrAuthLoginFailed:        true,
	ErrAuthRefreshFailed:      true,
	ErrAuthKeyInvalid:         true,
	// ErrAuthKeyMissing intentionally omitted: codeToStatus maps it to HTTP 500
	// (infrastructure misconfiguration). Including it here would cause
	// AuthMiddleware to downgrade an infra fault to Warn, masking the outage.
	// ErrAuthVerifierConfig is likewise 500 and must not appear here.

	// 403 — forbidden
	ErrAuthForbidden:             true,
	ErrCSRFOriginDenied:          true,
	ErrAuthPasswordResetRequired: true,
	ErrAuthSelfDelete:            true,
	ErrAuthUserLocked:            true,

	// 404 — resource not found
	ErrSessionNotFound:    true,
	ErrOrderNotFound:      true,
	ErrDeviceNotFound:     true,
	ErrCommandNotFound:    true,
	ErrAuthUserNotFound:   true,
	ErrAuthRoleNotFound:   true,
	ErrMetadataNotFound:   true,
	ErrCellNotFound:       true,
	ErrSliceNotFound:      true,
	ErrContractNotFound:   true,
	ErrAssemblyNotFound:   true,
	ErrJourneyNotFound:    true,
	ErrConfigNotFound:     true,
	ErrConfigRepoNotFound: true,
	ErrFlagNotFound:       true,
	ErrAuditRepoNotFound:  true,
	ErrWSConnNotFound:     true,

	// 409 — conflict
	ErrSessionConflict:       true,
	ErrAuthUserDuplicate:     true,
	ErrAuthRoleDuplicate:     true,
	ErrConfigDuplicate:       true,
	ErrConfigRepoDuplicate:   true,
	ErrFlagDuplicate:         true,
	ErrAuthRefreshTokenReuse: true,

	// 413 — payload too large
	ErrBodyTooLarge: true,

	// 429 — rate limited
	ErrRateLimited: true,
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
	return expected4xxCodes[ec.Code]
}
