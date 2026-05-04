package errcode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewWrapAndOptions(t *testing.T) {
	cause := errors.New("pool exhausted")
	err := Wrap(
		KindUnavailable,
		ErrServiceUnavailable,
		"service unavailable",
		cause,
		WithInternal("postgres pool exhausted"),
		WithDetails(map[string]any{"retry": true}),
		WithCategory(CategoryInfra),
	)

	assert.Equal(t, KindUnavailable, err.Kind)
	assert.Equal(t, ErrServiceUnavailable, err.Code)
	assert.Equal(t, "service unavailable", err.Message)
	assert.Equal(t, "postgres pool exhausted", err.InternalMessage)
	assert.Equal(t, map[string]any{"retry": true}, err.Details)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "[ERR_SERVICE_UNAVAILABLE] postgres pool exhausted: pool exhausted", err.Error())
}

func TestKindStatusAndPublicCode(t *testing.T) {
	cases := []struct {
		kind       Kind
		status     int
		publicCode Code
		client     bool
	}{
		{KindInvalid, http.StatusBadRequest, ErrInternal, true},
		{KindUnauthenticated, http.StatusUnauthorized, ErrInternal, true},
		{KindPermissionDenied, http.StatusForbidden, ErrInternal, true},
		{KindNotFound, http.StatusNotFound, ErrInternal, true},
		{KindConflict, http.StatusConflict, ErrInternal, true},
		{KindGone, http.StatusGone, ErrInternal, true},
		{KindPayloadTooLarge, http.StatusRequestEntityTooLarge, ErrInternal, true},
		{KindRateLimited, http.StatusTooManyRequests, ErrInternal, true},
		{KindClientClosed, StatusClientClosedRequest, ErrInternal, true},
		{KindUnavailable, http.StatusServiceUnavailable, ErrServiceUnavailable, false},
		{KindDeadlineExceeded, http.StatusGatewayTimeout, ErrServerTimeout, false},
		{KindInternal, http.StatusInternalServerError, ErrInternal, false},
		{KindNotImplemented, http.StatusNotImplemented, ErrInternal, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			assert.Equal(t, tc.status, tc.kind.Status())
			assert.Equal(t, tc.publicCode, tc.kind.PublicCode())
			assert.Equal(t, tc.client, tc.kind.IsClient())
		})
	}
}

func TestErrorStatusAndPublicCode(t *testing.T) {
	clientErr := New(KindNotFound, ErrCellNotFound, "cell not found")
	assert.Equal(t, http.StatusNotFound, clientErr.Status())
	assert.Equal(t, ErrCellNotFound, clientErr.PublicCode())

	serverErr := New(KindUnavailable, ErrKeyProviderTransient, "vault sealed")
	assert.Equal(t, http.StatusServiceUnavailable, serverErr.Status())
	assert.Equal(t, ErrServiceUnavailable, serverErr.PublicCode())
}

func TestIsInfraError(t *testing.T) {
	assert.False(t, IsInfraError(nil))
	assert.True(t, IsInfraError(context.Canceled))
	assert.True(t, IsInfraError(context.DeadlineExceeded))
	assert.True(t, IsInfraError(errors.New("plain")))
	assert.True(t, IsInfraError(New(KindInternal, ErrInternal, "db", WithCategory(CategoryInfra))))
	assert.True(t, IsInfraError(New(KindInternal, ErrInternal, "unknown")))
	assert.False(t, IsInfraError(New(KindNotFound, ErrSessionNotFound, "missing", WithCategory(CategoryDomain))))
	assert.False(t, IsInfraError(New(KindInvalid, ErrValidationFailed, "bad", WithCategory(CategoryValidation))))
	assert.False(t, IsInfraError(New(KindUnauthenticated, ErrAuthUnauthorized, "no", WithCategory(CategoryAuth))))
}

func TestIsDomainNotFound(t *testing.T) {
	domain := New(KindNotFound, ErrSessionNotFound, "missing", WithCategory(CategoryDomain))
	assert.True(t, IsDomainNotFound(domain, ErrSessionNotFound))
	assert.False(t, IsDomainNotFound(domain, ErrOrderNotFound))
	assert.False(t, IsDomainNotFound(New(KindNotFound, ErrSessionNotFound, "missing"), ErrSessionNotFound))
}

func TestIsTransientAndExpected4xx(t *testing.T) {
	assert.True(t, IsTransient(New(KindUnavailable, ErrKeyProviderTransient, "vault sealed")))
	assert.True(t, IsTransient(fmt.Errorf("wrap: %w", New(KindUnavailable, ErrKeyProviderTransient, "vault sealed"))))
	assert.False(t, IsTransient(New(KindInternal, ErrKeyProviderEncryptFailed, "encrypt failed")))

	assert.True(t, IsExpected4xx(New(KindInvalid, ErrValidationFailed, "bad")))
	assert.True(t, IsExpected4xx(New(KindUnauthenticated, ErrAuthUnauthorized, "no")))
	assert.False(t, IsExpected4xx(New(KindInternal, ErrInternal, "boom")))
}

func TestPublicCodeForStatus(t *testing.T) {
	assert.Equal(t, ErrInternal, PublicCodeForStatus(http.StatusInternalServerError))
	assert.Equal(t, ErrServiceUnavailable, PublicCodeForStatus(http.StatusServiceUnavailable))
	assert.Equal(t, ErrServerTimeout, PublicCodeForStatus(http.StatusGatewayTimeout))
}
