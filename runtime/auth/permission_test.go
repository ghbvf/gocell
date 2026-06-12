package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// --- WithAuthorizer / AuthorizerFromContext round-trip ---

func TestWithAuthorizer_RoundTrip(t *testing.T) {
	mock := &mockAuthorizer{allowed: true}
	ctx := WithAuthorizer(context.Background(), mock)

	got, ok := AuthorizerFromContext(ctx)
	assert.True(t, ok, "AuthorizerFromContext must return ok=true after WithAuthorizer")
	assert.Equal(t, mock, got, "AuthorizerFromContext must return the same Authorizer injected by WithAuthorizer")
}

func TestAuthorizerFromContext_Absent(t *testing.T) {
	got, ok := AuthorizerFromContext(context.Background())
	assert.False(t, ok, "AuthorizerFromContext must return ok=false when no Authorizer in context")
	assert.Nil(t, got, "AuthorizerFromContext must return nil Authorizer when absent")
}

// --- RequirePermission: allow path ---

func TestRequirePermission_Allow(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}
	mock := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	policy := RequirePermission(authz.PermAuditRead)
	err := policy(req)
	assert.NoError(t, err, "allow decision must return nil")
}

// --- RequirePermission: deny path → 403 ErrAuthForbidden ---

func TestRequirePermission_Deny(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"viewer"}}
	mock := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	policy := RequirePermission(authz.PermAuditRead)
	err := policy(req)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "deny must return an errcode.Error")
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}

// --- RequirePermission: Authorizer returns KindUnavailable error → transparent passthrough ---

func TestRequirePermission_AuthorizerError_Passthrough(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "policy store down")
	mock := &mockAuthorizer{err: unavailErr}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	policy := RequirePermission(authz.PermAuditRead)
	err := policy(req)
	require.Error(t, err)

	// Error must be passed through verbatim so httputil maps KindUnavailable → 503.
	assert.True(t, errors.Is(err, unavailErr), "authorizer error must be returned verbatim")
}

// --- RequirePermission: no Authorizer in ctx → fail-closed (403) ---

func TestRequirePermission_NoAuthorizer_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	// Only Principal injected — no Authorizer.
	req = req.WithContext(WithPrincipal(req.Context(), p))

	policy := RequirePermission(authz.PermAuditRead)
	err := policy(req)
	require.Error(t, err, "absent Authorizer must not silently permit")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"absent Authorizer must return PermissionDenied (403), not allow")
}

// --- RequirePermission: no Principal in ctx → 401 ---

func TestRequirePermission_NoPrincipal_Unauthenticated(t *testing.T) {
	mock := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	// Only Authorizer injected — no Principal.
	req = req.WithContext(WithAuthorizer(req.Context(), mock))

	policy := RequirePermission(authz.PermAuditRead)
	err := policy(req)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"absent Principal must return Unauthenticated (401)")
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code)
}

// --- RequirePermission: Principal with empty Subject → 401 ---

func TestRequirePermission_EmptySubject_Unauthenticated(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: ""}
	mock := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	policy := RequirePermission(authz.PermAuditRead)
	// FromContext will return (nil, false) for PrincipalUser with empty Subject
	// because principalHasAnyRole guard; but actually FromContext only filters
	// PrincipalUnknown. RequirePermission itself must check for empty subject.
	//
	// Expectation: empty subject for PrincipalUser → treat as unauthenticated.
	err := policy(req)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind)
}

// --- RequirePermission: zero Permission → fail-closed (403) ---

// TestRequirePermission_ZeroPermission_FailClosed asserts that passing a zero
// authz.Permission{} to RequirePermission causes a 403 even when an
// allow-everything Authorizer is in context. A zero Permission is a programmer
// error and must never reach the PDP.
func TestRequirePermission_ZeroPermission_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}
	allowAll := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), allowAll))

	policy := RequirePermission(authz.Permission{}) // zero value
	err := policy(req)
	require.Error(t, err, "zero Permission must fail-closed even with an allow-everything authorizer")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "zero Permission must return an errcode.Error")
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"zero Permission must return PermissionDenied (403), not allow")
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}

// --- Full flow via httptest handler: integration check ---

// TestRequirePermission_HTTPIntegration_AllowDeny exercises the Policy through a
// real HTTP handler using httputil.WriteError so the JSON response body matches the
// canonical errcode envelope. The deny case produces ERR_AUTH_FORBIDDEN (403),
// and the unavailable case produces ERR_SERVICE_UNAVAILABLE (503).
func TestRequirePermission_HTTPIntegration_AllowDeny(t *testing.T) {
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "policy store down")
	tests := []struct {
		name       string
		authorizer *mockAuthorizer
		wantCode   int
		wantErr    string // empty = no error body check
	}{
		{"allow", &mockAuthorizer{allowed: true}, http.StatusOK, ""},
		{"deny", &mockAuthorizer{allowed: false}, http.StatusForbidden, "ERR_AUTH_FORBIDDEN"},
		{"unavailable", &mockAuthorizer{err: unavailErr}, http.StatusServiceUnavailable, "ERR_SERVICE_UNAVAILABLE"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := RequirePermission(authz.PermAuditRead)
			p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := policy(r); err != nil {
					httputil.WriteError(r.Context(), w, err)
					return
				}
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
			req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), tc.authorizer))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantCode, rec.Code)
			if tc.wantErr != "" {
				assertErrorCode(t, rec, tc.wantErr)
			}
		})
	}
}
