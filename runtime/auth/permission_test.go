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
	"github.com/ghbvf/gocell/pkg/tenant"
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

	policy := RequirePermission(authz.PermAuditRead())
	err := policy(req)
	assert.NoError(t, err, "allow decision must return nil")
}

// --- RequirePermission: deny path → 403 ErrAuthForbidden ---

func TestRequirePermission_Deny(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"viewer"}}
	mock := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	policy := RequirePermission(authz.PermAuditRead())
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

	policy := RequirePermission(authz.PermAuditRead())
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

	policy := RequirePermission(authz.PermAuditRead())
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

	policy := RequirePermission(authz.PermAuditRead())
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

	policy := RequirePermission(authz.PermAuditRead())
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

// --- RequirePermission: Allow with non-zero obligations → fail-closed (403) ---

// TestRequirePermission_AllowWithObligations_FailClosed asserts F5: an Allow
// decision carrying a non-zero obligation (which this coarse route gate cannot
// discharge) is denied rather than silently dropped. A baseline allow carries
// zero obligations and passes (covered by TestRequirePermission_Allow); this
// pins the tenant-allow-with-obligation case.
func TestRequirePermission_AllowWithObligations_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "u1", Roles: []string{"admin"}}
	// Allow, but with a RowScope obligation the route gate cannot enforce.
	mock := &mockAuthorizer{allowed: true, obligations: authz.Obligations{RowScope: tenant.RowScopeSelf}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	err := RequirePermission(authz.PermAuditRead())(req)
	require.Error(t, err, "Allow with unenforceable obligations must fail-closed, not silently drop")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"Allow with non-zero obligations must return PermissionDenied (403)")
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
			policy := RequirePermission(authz.PermAuditRead())
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

// --- RequirePermissionOrSelf: request-shape self-exemption + PDP delegation ---
//
// The self-exemption and the PDP delegation form a non-vacuous pair: inverting
// the self-check would flip both TestRequirePermissionOrSelf_Self_ExemptWithoutPDP
// (self would hit the deny PDP → 403) and _NonSelfNonAdmin_PDPDeny (non-self would
// exempt → nil), so neither can pass while the other is broken.

const (
	roselfSubjectA = "11111111-1111-1111-1111-111111111111"
	roselfOtherB   = "22222222-2222-2222-2222-222222222222"
)

func TestRequirePermissionOrSelf_Self_ExemptWithoutPDP(t *testing.T) {
	// A subject naming itself in the path is exempt: even a DENY Authorizer must
	// not be consulted — the gate returns nil before delegating to the PDP.
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	assert.NoError(t, err, "self-access (param==subject) must be exempt without consulting the PDP")
}

func TestRequirePermissionOrSelf_NonSelfAdmin_PDPAllow(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: "admin-1", Roles: []string{"admin"}}
	allow := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfOtherB, nil)
	req.SetPathValue("id", roselfOtherB)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), allow))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	assert.NoError(t, err, "non-self admin must pass via PDP allow")
}

func TestRequirePermissionOrSelf_NonSelfNonAdmin_PDPDeny(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfOtherB, nil)
	req.SetPathValue("id", roselfOtherB)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "deny must return an errcode.Error")
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "non-self non-admin must be denied by the PDP (403)")
}

func TestRequirePermissionOrSelf_NonSelfNoAuthorizer_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfOtherB, nil)
	req.SetPathValue("id", roselfOtherB)
	req = req.WithContext(WithPrincipal(req.Context(), p)) // no Authorizer wired

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "non-self with no Authorizer must fail closed (403)")
}

func TestRequirePermissionOrSelf_EmptyParam_NotExempt(t *testing.T) {
	// Empty path value ≠ self (tenancy.md): must fall through to the PDP. A deny
	// Authorizer (403) proves the PDP was consulted rather than self-exempted.
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/", nil)
	// no SetPathValue("id", ...) → empty path value
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err, "empty param must not self-exempt; PDP deny → 403")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
}

func TestRequirePermissionOrSelf_NonUUIDParamMismatch_NotExempt(t *testing.T) {
	// A non-UUID path value that does not equal the subject must not self-exempt;
	// it falls through to the PDP (deny → 403).
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/not-a-uuid", nil)
	req.SetPathValue("id", "not-a-uuid")
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err, "non-self non-UUID param must not self-exempt; PDP deny → 403")
}

func TestRequirePermissionOrSelf_ServicePrincipal_NotExempt(t *testing.T) {
	// A non-user principal never self-exempts even if its Subject coincides with
	// the path value — service/anonymous fall through to the PDP (fail-closed).
	p := &Principal{Kind: PrincipalService, Subject: roselfSubjectA}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err, "service principal must not self-exempt; PDP deny → 403")
}

// TestRequirePermissionOrSelf_NonSelf_AllowWithObligations_FailClosed asserts F5
// for the non-self delegation path: when RequirePermissionOrSelf falls through to
// the PDP (subject != path id) and the PDP returns Allow with a non-zero obligation,
// the gate must deny (403) rather than silently drop the obligation. This mirrors
// TestRequirePermission_AllowWithObligations_FailClosed for the RequirePermission
// gate — the obligation-fail-closed invariant must hold on both paths.
func TestRequirePermissionOrSelf_NonSelf_AllowWithObligations_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"admin"}}
	// Allow, but with a RowScope obligation the route gate cannot enforce.
	mock := &mockAuthorizer{allowed: true, obligations: authz.Obligations{RowScope: tenant.RowScopeSelf}}

	// Non-self path: subject (A) != path param (B), so the self-exemption branch
	// is skipped and the request is delegated to the PDP (mock).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfOtherB, nil)
	req.SetPathValue("id", roselfOtherB)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	err := RequirePermissionOrSelf("id", authz.PermUserRead())(req)
	require.Error(t, err, "Allow with unenforceable obligations must fail-closed, not silently drop")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"Allow with non-zero obligations must return PermissionDenied (403)")
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}
