package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

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

	err := RequirePermissionForContract("http.config.get.v1", r)(req)
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

	err := RequirePermissionForContract("http.config.get.v1", r)(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "PDP deny must return 403")
}

// TestRequirePermissionForContract_ResolverMiss_Panics: a contract id the cell resolver
// does not know is codegen drift (the handler is only generated with this gate when
// the contract carries a permission overlay, and cellgen enrolls every such contract
// in the resolver). Resolution is one-shot at construction (Mount/Init time), so the
// miss fails fast there, not at first request.
func TestRequirePermissionForContract_ResolverMiss_Panics(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	assert.Panics(t, func() {
		_ = RequirePermissionForContract("http.unmapped.v1", r)
	}, "resolver miss at construction must fail-fast (codegen drift)")
}

// TestRequirePermissionForContract_NoAuthorizer_FailClosed: resolving the permission
// does not relax the fail-closed contract — an unwired PDP still denies (403).
func TestRequirePermissionForContract_NoAuthorizer_FailClosed(t *testing.T) {
	r := NewStaticMethodPolicyResolver(map[string]string{"http.config.get.v1": "config:read"})
	p := &Principal{Kind: PrincipalUser, Subject: "11111111-1111-1111-1111-111111111111", Roles: []string{"admin"}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/x", nil)
	req = req.WithContext(WithPrincipal(req.Context(), p)) // no Authorizer wired

	err := RequirePermissionForContract("http.config.get.v1", r)(req)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind, "unwired PDP must fail-closed (403)")
}
