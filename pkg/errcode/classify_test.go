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
func TestIsDomainNotFound(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		codes []string
		want  bool
	}{
		{
			name:  "nil error",
			err:   nil,
			codes: []string{string(ErrSessionNotFound)},
			want:  false,
		},
		{
			name:  "domain not-found code in whitelist",
			err:   NewDomain(ErrSessionNotFound, "session not found"),
			codes: []string{string(ErrSessionNotFound)},
			want:  true,
		},
		{
			name:  "domain not-found code not in whitelist",
			err:   NewDomain(ErrSessionNotFound, "session not found"),
			codes: []string{string(ErrOrderNotFound)},
			want:  false,
		},
		{
			name:  "infra error is not domain not-found",
			err:   NewInfra(ErrInternal, "db down"),
			codes: []string{string(ErrInternal)},
			want:  false,
		},
		{
			name:  "plain error is not domain not-found",
			err:   errors.New("connection refused"),
			codes: []string{},
			want:  false,
		},
		{
			name:  "errcode with CategoryUnspecified is not domain not-found",
			err:   New(ErrSessionNotFound, "not found"),
			codes: []string{string(ErrSessionNotFound)},
			want:  false,
		},
		{
			name:  "multiple whitelist codes — first matches",
			err:   NewDomain(ErrSessionNotFound, "not found"),
			codes: []string{string(ErrSessionNotFound), string(ErrOrderNotFound)},
			want:  true,
		},
		{
			name:  "multiple whitelist codes — second matches",
			err:   NewDomain(ErrOrderNotFound, "order not found"),
			codes: []string{string(ErrSessionNotFound), string(ErrOrderNotFound)},
			want:  true,
		},
		{
			name:  "empty code whitelist never matches",
			err:   NewDomain(ErrSessionNotFound, "not found"),
			codes: []string{},
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
