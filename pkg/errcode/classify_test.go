package errcode

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsInfraError covers the fail-closed dual-channel classifier.
// Unrecognised plain errors are treated as infra (fail-closed) to prevent
// leaking infra faults into domain-not-found branches.
func TestIsInfraError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not infra",
			err:  nil,
			want: false,
		},
		{
			name: "context.Canceled is infra",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "context.DeadlineExceeded is infra",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "driver.ErrBadConn is infra",
			err:  driver.ErrBadConn,
			want: true,
		},
		{
			name: "sql.ErrConnDone is infra",
			err:  sql.ErrConnDone,
			want: true,
		},
		{
			name: "wrapped context.Canceled is infra",
			err:  errors.Join(errors.New("outer"), context.Canceled),
			want: true,
		},
		{
			name: "errcode with CategoryInfra is infra",
			err:  &Error{Code: ErrInternal, Message: "db down", Category: CategoryInfra},
			want: true,
		},
		{
			name: "errcode with CategoryDomain is not infra",
			err:  &Error{Code: ErrSessionNotFound, Message: "not found", Category: CategoryDomain},
			want: false,
		},
		{
			name: "errcode with CategoryAuth is not infra",
			err:  &Error{Code: ErrAuthUnauthorized, Message: "unauthorized", Category: CategoryAuth},
			want: false,
		},
		{
			name: "errcode with CategoryValidation is not infra",
			err:  &Error{Code: ErrValidationFailed, Message: "bad input", Category: CategoryValidation},
			want: false,
		},
		{
			name: "errcode with CategoryUnspecified (zero value) is fail-closed infra",
			err:  &Error{Code: ErrInternal, Message: "unclassified"},
			want: true,
		},
		{
			name: "plain unclassified error is fail-closed infra",
			err:  errors.New("some unknown error"),
			want: true,
		},
		{
			name: "NewInfra creates infra error",
			err:  NewInfra(ErrInternal, "storage unavailable"),
			want: true,
		},
		{
			name: "NewDomain creates domain error (not infra)",
			err:  NewDomain(ErrSessionNotFound, "session not found"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsInfraError(tt.err))
		})
	}
}

// TestIsDomainNotFound covers the whitelist-based domain-not-found classifier.
// P2-1: IsDomainNotFound now accepts ...Code instead of ...string, so callers
// pass Code constants directly without string(...) conversion.
func TestIsDomainNotFound(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		codes []Code
		want  bool
	}{
		{
			name:  "nil error",
			err:   nil,
			codes: []Code{ErrSessionNotFound},
			want:  false,
		},
		{
			name:  "domain not-found code in whitelist",
			err:   NewDomain(ErrSessionNotFound, "session not found"),
			codes: []Code{ErrSessionNotFound},
			want:  true,
		},
		{
			name:  "domain not-found code not in whitelist",
			err:   NewDomain(ErrSessionNotFound, "session not found"),
			codes: []Code{ErrOrderNotFound},
			want:  false,
		},
		{
			name:  "infra error is not domain not-found",
			err:   NewInfra(ErrInternal, "db down"),
			codes: []Code{ErrInternal},
			want:  false,
		},
		{
			name:  "plain error is not domain not-found",
			err:   errors.New("connection refused"),
			codes: []Code{},
			want:  false,
		},
		{
			name:  "errcode with CategoryUnspecified is not domain not-found",
			err:   New(ErrSessionNotFound, "not found"),
			codes: []Code{ErrSessionNotFound},
			want:  false,
		},
		{
			name:  "multiple whitelist codes — first matches",
			err:   NewDomain(ErrSessionNotFound, "not found"),
			codes: []Code{ErrSessionNotFound, ErrOrderNotFound},
			want:  true,
		},
		{
			name:  "multiple whitelist codes — second matches",
			err:   NewDomain(ErrOrderNotFound, "order not found"),
			codes: []Code{ErrSessionNotFound, ErrOrderNotFound},
			want:  true,
		},
		{
			name:  "empty code whitelist never matches",
			err:   NewDomain(ErrSessionNotFound, "not found"),
			codes: []Code{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDomainNotFound(tt.err, tt.codes...))
		})
	}
}

// TestIsExpected4xx covers the HTTP 4xx classification for log-level routing.
func TestIsExpected4xx(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not expected 4xx",
			err:  nil,
			want: false,
		},
		{
			name: "ErrAuthUnauthorized (401) is expected 4xx",
			err:  New(ErrAuthUnauthorized, "unauthorized"),
			want: true,
		},
		{
			name: "ErrAuthForbidden (403) is expected 4xx",
			err:  New(ErrAuthForbidden, "forbidden"),
			want: true,
		},
		{
			name: "ErrSessionNotFound (404) is expected 4xx",
			err:  New(ErrSessionNotFound, "not found"),
			want: true,
		},
		{
			name: "ErrAuthTokenInvalid (401) is expected 4xx",
			err:  New(ErrAuthTokenInvalid, "invalid token"),
			want: true,
		},
		{
			name: "ErrAuthTokenExpired (401) is expected 4xx",
			err:  New(ErrAuthTokenExpired, "expired"),
			want: true,
		},
		{
			name: "ErrValidationFailed (400) is expected 4xx",
			err:  New(ErrValidationFailed, "bad input"),
			want: true,
		},
		{
			name: "ErrSessionConflict (409) is expected 4xx",
			err:  New(ErrSessionConflict, "conflict"),
			want: true,
		},
		{
			name: "ErrInternal (500) is NOT expected 4xx",
			err:  New(ErrInternal, "internal error"),
			want: false,
		},
		{
			name: "plain error is NOT expected 4xx",
			err:  errors.New("some error"),
			want: false,
		},
		{
			name: "ErrAuthInvalidToken (401) is expected 4xx",
			err:  New(ErrAuthInvalidToken, "invalid"),
			want: true,
		},
		{
			name: "ErrAuthLoginFailed (401) is expected 4xx",
			err:  New(ErrAuthLoginFailed, "login failed"),
			want: true,
		},
		{
			name: "ErrRateLimited (429) is expected 4xx",
			err:  New(ErrRateLimited, "too many requests"),
			want: true,
		},
		{
			name: "ErrBodyTooLarge (413) is expected 4xx",
			err:  New(ErrBodyTooLarge, "body too large"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsExpected4xx(tt.err))
		})
	}
}

// TestNewInfra_CategoryField verifies the Category field is set correctly.
func TestNewInfra_CategoryField(t *testing.T) {
	err := NewInfra(ErrInternal, "db unavailable")
	assert.Equal(t, CategoryInfra, err.Category)
	assert.Equal(t, ErrInternal, err.Code)
	assert.Equal(t, "db unavailable", err.Message)
}

// TestNewDomain_CategoryField verifies NewDomain sets CategoryDomain.
func TestNewDomain_CategoryField(t *testing.T) {
	err := NewDomain(ErrSessionNotFound, "session not found")
	assert.Equal(t, CategoryDomain, err.Category)
	assert.Equal(t, ErrSessionNotFound, err.Code)
}

// TestNew_BackwardCompatibility verifies that the existing New() constructor
// produces CategoryUnspecified (zero value), preserving all prior behaviour.
func TestNew_BackwardCompatibility(t *testing.T) {
	err := New(ErrCellNotFound, "not found")
	assert.Equal(t, CategoryUnspecified, err.Category,
		"New() must preserve zero-value Category for backward compat")
	// Unspecified is fail-closed → treated as infra.
	assert.True(t, IsInfraError(err),
		"CategoryUnspecified is fail-closed and must be treated as infra")
}

// TestWrapInfra verifies that WrapInfra sets CategoryInfra and preserves the cause.
// P2-2: WrapInfra / WrapDomain are convenience constructors for cause-preserving
// classified errors, complementing NewInfra / NewDomain for the no-cause case.
func TestWrapInfra(t *testing.T) {
	cause := errors.New("connection refused")
	err := WrapInfra(ErrInternal, "db unavailable", cause)

	assert.Equal(t, CategoryInfra, err.Category)
	assert.Equal(t, ErrInternal, err.Code)
	assert.Equal(t, "db unavailable", err.Message)
	assert.Equal(t, cause, err.Cause, "cause must be preserved")
	assert.True(t, IsInfraError(err), "WrapInfra result must be classified as infra")
	// Unwrap chain: errors.Is must reach the cause.
	assert.True(t, errors.Is(err, cause), "errors.Is must traverse Cause via Unwrap")
}

// TestWrapDomain verifies that WrapDomain sets CategoryDomain and preserves the cause.
func TestWrapDomain(t *testing.T) {
	cause := errors.New("underlying repo error")
	err := WrapDomain(ErrSessionNotFound, "session not found", cause)

	assert.Equal(t, CategoryDomain, err.Category)
	assert.Equal(t, ErrSessionNotFound, err.Code)
	assert.Equal(t, "session not found", err.Message)
	assert.Equal(t, cause, err.Cause, "cause must be preserved")
	assert.False(t, IsInfraError(err), "WrapDomain result must not be classified as infra")
	assert.True(t, IsDomainNotFound(err, ErrSessionNotFound),
		"WrapDomain result must be classified as domain not-found when code matches")
	// Unwrap chain: errors.Is must reach the cause.
	assert.True(t, errors.Is(err, cause), "errors.Is must traverse Cause via Unwrap")
}

// TestIsExpected4xx_ErrMetadataNotFound verifies P2-5: ErrMetadataNotFound maps
// to HTTP 404 in codeToStatus and belongs in the expected4xxCodes whitelist.
func TestIsExpected4xx_ErrMetadataNotFound(t *testing.T) {
	err := New(ErrMetadataNotFound, "metadata not found")
	assert.True(t, IsExpected4xx(err),
		"ErrMetadataNotFound (404) must be in expected4xxCodes whitelist")
}

// TestIsExpected4xx_ErrAuthKeyMissing_IsNotExpected4xx verifies P1-1:
// ErrAuthKeyMissing maps to HTTP 500 in codeToStatus (infra misconfiguration)
// and must NOT appear in the expected4xxCodes whitelist.
func TestIsExpected4xx_ErrAuthKeyMissing_IsNotExpected4xx(t *testing.T) {
	err := New(ErrAuthKeyMissing, "no signing key configured")
	assert.False(t, IsExpected4xx(err),
		"ErrAuthKeyMissing (500/infra) must NOT be in expected4xxCodes whitelist")
}

// TestNewAuth verifies that NewAuth creates an *Error with CategoryAuth, the
// supplied code and message, and a nil cause.
func TestNewAuth(t *testing.T) {
	tests := []struct {
		name    string
		code    Code
		message string
	}{
		{
			name:    "basic auth error",
			code:    "ERR_FOO",
			message: "msg",
		},
		{
			name:    "reuse detection signal",
			code:    ErrAuthRefreshTokenReuse,
			message: "refresh token reuse detected",
		},
		{
			name:    "unauthorized",
			code:    ErrAuthUnauthorized,
			message: "unauthorized access",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewAuth(tt.code, tt.message)
			assert.Equal(t, tt.code, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, CategoryAuth, err.Category)
			assert.Nil(t, err.Cause, "NewAuth must have nil cause")
			// CategoryAuth is not infra (fail-closed check).
			assert.False(t, IsInfraError(err), "CategoryAuth must not be classified as infra")
		})
	}
}

// TestWrapAuth verifies that WrapAuth creates an *Error with CategoryAuth that
// wraps the supplied cause and exposes it via errors.Is / errors.Unwrap.
func TestWrapAuth(t *testing.T) {
	baseErr := errors.New("underlying auth failure")

	tests := []struct {
		name    string
		code    Code
		message string
		cause   error
	}{
		{
			name:    "wraps base error",
			code:    "ERR_FOO",
			message: "msg",
			cause:   baseErr,
		},
		{
			name:    "reuse detection with cause",
			code:    ErrAuthRefreshTokenReuse,
			message: "token reuse: RFC 6749 §10.4",
			cause:   errors.New("duplicate jti"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := WrapAuth(tt.code, tt.message, tt.cause)
			assert.Equal(t, tt.code, wrapped.Code)
			assert.Equal(t, tt.message, wrapped.Message)
			assert.Equal(t, CategoryAuth, wrapped.Category)
			assert.Equal(t, tt.cause, wrapped.Cause, "Cause field must be preserved")
			assert.True(t, errors.Is(wrapped, tt.cause),
				"errors.Is must traverse Cause via Unwrap")
			assert.Equal(t, tt.cause, errors.Unwrap(wrapped),
				"errors.Unwrap must return the direct cause")
			// CategoryAuth is not infra.
			assert.False(t, IsInfraError(wrapped), "CategoryAuth must not be classified as infra")
		})
	}
}
