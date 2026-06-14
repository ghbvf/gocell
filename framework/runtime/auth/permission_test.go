package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
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

// --- RequirePermissionForResource: PDP-based ownership gate (#1977 Batch B) ---
//
// RequirePermissionForResource replaces RequirePermissionOrSelf. Self-access is
// now decided by the PDP (baseline ownership rule subject.sub == resource.id), not
// by a Go request-shape short-circuit. These tests pin the new behavior.

const (
	roselfSubjectA = "11111111-1111-1111-1111-111111111111"
	roselfOtherB   = "22222222-2222-2222-2222-222222222222"
)

// captureAuthorizer records the resource argument forwarded to Authorize so tests
// can assert canonicalization. It lives in permission_test.go to avoid polluting
// the shared mockAuthorizer in middleware_test.go.
type captureAuthorizer struct {
	gotResource string
	allowed     bool
}

func (a *captureAuthorizer) Authorize(_ context.Context, _, resource, _ string) (authz.Decision, error) {
	a.gotResource = resource
	if a.allowed {
		dec, err := authz.Allow(authz.Obligations{})
		return dec, err
	}
	return authz.Deny("test: denied"), nil
}

// TestRequirePermissionForResource_ForwardsCanonicalResource is the
// canonicalization regression guard: even if the URL carries an UPPERCASE UUID
// path value, the gate must canonicalize it before forwarding to the PDP as
// resource. (The deleted isSelfAccess canonicalized both sides; we now
// canonicalize at the gate via httputil.ParseCanonicalUUID.)
func TestRequirePermissionForResource_ForwardsCanonicalResource(t *testing.T) {
	upper := strings.ToUpper(roselfSubjectA)
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	cap := &captureAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+upper, nil)
	req.SetPathValue("id", upper)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), cap))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	assert.NoError(t, err)
	assert.Equal(t, roselfSubjectA, cap.gotResource,
		"gate must forward canonical (lowercase) UUID to PDP; got %q, want %q", cap.gotResource, roselfSubjectA)
}

// TestRequirePermissionForResource_PDPAllow verifies that a PDP Allow returns nil.
func TestRequirePermissionForResource_PDPAllow(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	allow := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), allow))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	assert.NoError(t, err, "PDP allow must return nil")
}

// TestRequirePermissionForResource_PDPDeny verifies that a PDP Deny returns KindPermissionDenied.
func TestRequirePermissionForResource_PDPDeny(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfOtherB, nil)
	req.SetPathValue("id", roselfOtherB)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "PDP deny must return 403")
}

// TestRequirePermissionForResource_NoAuthorizer_FailClosed is the key semantic
// change from Batch B: self-access is no longer a Go short-circuit. Without a
// wired Authorizer the gate fails closed (403) even when subject == path param.
func TestRequirePermissionForResource_NoAuthorizer_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	// No Authorizer wired — self-naming subject must NOT be exempt.
	req = req.WithContext(WithPrincipal(req.Context(), p))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	require.Error(t, err, "absent Authorizer must fail-closed (403) even for self-naming subject")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"absent Authorizer must return 403, not allow self-access without PDP")
}

// TestRequirePermissionForResource_ZeroPermission_FailClosed verifies zero
// authz.Permission{} → 403 even with an allow-everything Authorizer.
func TestRequirePermissionForResource_ZeroPermission_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"admin"}}
	allow := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), allow))

	err := RequirePermissionForResource("id", authz.Permission{})(req)
	require.Error(t, err, "zero Permission must fail-closed even with allow-everything Authorizer")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}

// TestRequirePermissionForResource_NoPrincipal_Unauthenticated verifies no
// principal → 401.
func TestRequirePermissionForResource_NoPrincipal_Unauthenticated(t *testing.T) {
	allow := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(req.Context(), allow)) // no Principal

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind, "absent principal must return 401")
}

// TestRequirePermissionForResource_EmptyParam_PDPDeny verifies that an empty path
// value is FORWARDED to the PDP as resource="" (not short-circuited) and that the
// PDP deny results in 403. Using captureAuthorizer so we can assert both that the
// resource forwarded is "" AND that the result is a 403 (deny via captureAuthorizer
// returning deny). This proves non-short-circuit: the gate always reaches the PDP.
func TestRequirePermissionForResource_EmptyParam_PDPDeny(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"user"}}
	// captureAuthorizer with allowed=false: records what resource was forwarded, then denies.
	capAuth := &captureAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/", nil)
	// no SetPathValue("id", ...) → empty path value
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), capAuth))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	require.Error(t, err, "empty param → resource not-found → ownership rule can't fire → PDP deny → 403")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)

	// Core assertion: the empty param was FORWARDED to the PDP as resource=""
	// (non-short-circuit), not blocked before reaching the authorizer.
	assert.Equal(t, "", capAuth.gotResource,
		"empty path param must be forwarded to PDP as resource=\"\", not short-circuited")
}

// TestRequirePermissionForResource_AllowWithObligations_FailClosed asserts F5
// (mirrors TestRequirePermission_AllowWithObligations_FailClosed): Allow with
// non-zero obligations must fail-closed at this coarse route gate.
func TestRequirePermissionForResource_AllowWithObligations_FailClosed(t *testing.T) {
	p := &Principal{Kind: PrincipalUser, Subject: roselfSubjectA, Roles: []string{"admin"}}
	mock := &mockAuthorizer{allowed: true, obligations: authz.Obligations{RowScope: tenant.RowScopeSelf}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+roselfSubjectA, nil)
	req.SetPathValue("id", roselfSubjectA)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), mock))

	err := RequirePermissionForResource("id", authz.PermUserRead())(req)
	require.Error(t, err, "Allow with unenforceable obligations must fail-closed, not silently drop")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind,
		"Allow with non-zero obligations must return PermissionDenied (403)")
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}
