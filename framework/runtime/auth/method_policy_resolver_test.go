package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// coarseSpec builds a minimal HTTP ContractSpec carrying only the ID — the
// RequirePermissionForContract funnel reads ID/Resource/SelfScoped and does not
// Validate(), so a bare-ID spec exercises the coarse (default) branch.
func coarseSpec(id string) contractspec.ContractSpec {
	return contractspec.ContractSpec{ID: id}
}

// TestNewStaticMethodPolicyResolver_HitMiss pins the cell-level HTTP resolver:
// a mapped contract id resolves to its sealed Permission (ok=true); an unmapped
// key fails closed (ok=false), not an implicit allow.
func TestNewStaticMethodPolicyResolver_HitMiss(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{
		"http.config.get.v1":   "config:read",
		"http.config.write.v1": "config:write",
	})

	got, ok := r.PermissionForMethod("http.config.get.v1")
	require.True(t, ok, "mapped contract id must resolve")
	assert.Equal(t, authz.PermConfigRead(), got)

	got, ok = r.PermissionForMethod("http.config.write.v1")
	require.True(t, ok)
	assert.Equal(t, authz.PermConfigWrite(), got)

	_, ok = r.PermissionForMethod("http.not.mapped.v1")
	assert.False(t, ok, "unmapped key must fail closed (ok=false), never an implicit allow")

	// empty map: construction must not panic, and every lookup fails closed.
	empty := NewStaticMethodPolicyResolver(map[string]string{})
	_, ok = empty.PermissionForMethod("anything")
	assert.False(t, ok, "empty resolver must fail closed for any key")
}

// TestNewStaticMethodPolicyResolver_UnknownAction_Panics: an action string outside
// the closed authz registry is codegen/registry drift — the constructor fails fast
// (mirrors the gRPC registrar.recordMethodOverlays unknown-permission panic).
func TestNewStaticMethodPolicyResolver_UnknownAction_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewStaticMethodPolicyResolver(map[string]string{"http.x.v1": "not-a-real-action"})
	}, "unknown action string must fail-fast at construction")
}

// TestRequirePermissionForContract_Allow: the contract-derived HTTP gate resolves the
// permission through the resolver and then behaves exactly like RequirePermission
// — a PDP allow returns nil.
func TestRequirePermissionForContract_Allow(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	p := &Principal{Kind: PrincipalUser, Subject: "11111111-1111-1111-1111-111111111111", Roles: []string{"admin"}}
	allow := &mockAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/x", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), allow))

	err := RequirePermissionForContract(coarseSpec("http.config.get.v1"), r)(req)
	assert.NoError(t, err, "PDP allow must return nil")
}

// TestRequirePermissionForContract_Deny: a PDP deny returns KindPermissionDenied (403),
// same as RequirePermission (the gate is the same single PDP path).
func TestRequirePermissionForContract_Deny(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	p := &Principal{Kind: PrincipalUser, Subject: "11111111-1111-1111-1111-111111111111", Roles: []string{"user"}}
	deny := &mockAuthorizer{allowed: false}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/x", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), deny))

	err := RequirePermissionForContract(coarseSpec("http.config.get.v1"), r)(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "PDP deny must return 403")
}

// TestRequirePermissionForContract_WithoutResource_ForwardsURLPath: the coarse branch
// (no Resource, no SelfScoped) forwards r.URL.Path to the PDP — same as a hand-wired
// RequirePermission.
func TestRequirePermissionForContract_WithoutResource_ForwardsURLPath(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.policy.list.v1": "policy:read"})
	p := &Principal{Kind: PrincipalUser, Subject: "11111111-1111-1111-1111-111111111111", Roles: []string{"admin"}}
	cap := &captureAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), cap))

	err := RequirePermissionForContract(coarseSpec("http.policy.list.v1"), r)(req)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/policy", cap.gotResource,
		"coarse funnel must forward r.URL.Path to the PDP (got %q)", cap.gotResource)
}

// TestRequirePermissionForContract_WithResource_ForwardsPathParam: the owner-scoped
// branch (spec.Resource set) forwards the canonicalized path-param VALUE — not
// r.URL.Path — to the PDP, so the identity-ownership baseline (subject.sub ==
// resource.id) can fire. This is the contract-derived sibling of a hand-wired
// auth.RequirePermissionForResource("id", perm).
func TestRequirePermissionForContract_WithResource_ForwardsPathParam(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.auth.user.get.v1": "user:read"})
	subject := "11111111-1111-1111-1111-111111111111"
	resourceID := "22222222-2222-2222-2222-222222222222"
	p := &Principal{Kind: PrincipalUser, Subject: subject, Roles: []string{"user"}}
	cap := &captureAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/"+resourceID, nil)
	req.SetPathValue("id", resourceID)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), cap))

	spec := contractspec.ContractSpec{ID: "http.auth.user.get.v1", Resource: "id"}
	err := RequirePermissionForContract(spec, r)(req)
	require.NoError(t, err)
	assert.Equal(t, resourceID, cap.gotResource,
		"owner-scoped funnel must forward the path-param resource (not URL.Path); got %q", cap.gotResource)
}

// TestRequirePermissionForContract_SelfScoped_ForwardsSubject: the self-scoped branch
// (spec.SelfScoped) forwards the caller's OWN canonical subject to the PDP as the
// resource (the route carries no path param), so subject.sub == resource.id evaluates
// the caller against themselves. Contract-derived sibling of RequirePermissionForSelf.
func TestRequirePermissionForContract_SelfScoped_ForwardsSubject(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.auth.decide.v1": "access:decide"})
	subject := "33333333-3333-3333-3333-333333333333"
	p := &Principal{Kind: PrincipalUser, Subject: subject, Roles: []string{"user"}}
	cap := &captureAuthorizer{allowed: true}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/decide", nil)
	req = req.WithContext(WithAuthorizer(WithPrincipal(req.Context(), p), cap))

	spec := contractspec.ContractSpec{ID: "http.auth.decide.v1", SelfScoped: true}
	err := RequirePermissionForContract(spec, r)(req)
	require.NoError(t, err)
	assert.Equal(t, subject, cap.gotResource,
		"self-scoped funnel must forward the caller's own subject to the PDP; got %q", cap.gotResource)
}

// TestRequirePermissionForContract_ResolverMiss_Panics: a contract id the cell resolver
// does not know is codegen drift (the handler is only generated with this gate when
// the contract carries a permission overlay, and cellgen enrolls every such contract
// in the resolver). Resolution is one-shot at construction (Mount/Init time), so the
// miss fails fast there, not at first request.
func TestRequirePermissionForContract_ResolverMiss_Panics(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	assert.Panics(t, func() {
		_ = RequirePermissionForContract(coarseSpec("http.unmapped.v1"), r)
	}, "resolver miss at construction must fail-fast (codegen drift)")
}

// TestRequirePermissionForContract_NoAuthorizer_FailClosed: resolving the permission
// does not relax the fail-closed contract — an unwired PDP still denies (403).
func TestRequirePermissionForContract_NoAuthorizer_FailClosed(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	p := &Principal{Kind: PrincipalUser, Subject: "11111111-1111-1111-1111-111111111111", Roles: []string{"admin"}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/x", nil)
	req = req.WithContext(WithPrincipal(req.Context(), p)) // no Authorizer wired

	err := RequirePermissionForContract(coarseSpec("http.config.get.v1"), r)(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "unwired PDP must fail-closed (403)")
}

// nilGuardResolver is a pointer-receiver MethodPolicyResolver used only to construct
// a typed-nil interface value for the nil-guard test.
type nilGuardResolver struct{}

func (*nilGuardResolver) PermissionForMethod(string) (authz.Permission, bool) {
	return authz.Permission{}, false
}

// TestRequirePermissionForContract_NilResolver_Panics: the exported helper fails fast
// (panicregister, not a bare Go nil-deref) on both a nil interface and a typed-nil
// resolver — validation.IsNilInterface catches the typed-nil a bare == nil would miss.
func TestRequirePermissionForContract_NilResolver_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = RequirePermissionForContract(coarseSpec("http.config.get.v1"), nil)
	}, "nil interface resolver must fail-fast at construction")

	var typedNil *nilGuardResolver // typed-nil: non-nil interface, nil concrete pointer
	assert.Panics(t, func() {
		_ = RequirePermissionForContract(coarseSpec("http.config.get.v1"), typedNil)
	}, "typed-nil resolver must fail-fast (IsNilInterface), not slip to a bare nil-deref")
}
