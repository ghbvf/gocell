//go:build integration

package main

// TestABACPDPGatesAccesscore is the accesscore ABAC PDP acceptance test: the wired
// PDP must gate real HTTP traffic on accesscore policy / user / role endpoints.
// Self-access is a PDP OWNERSHIP DECISION (#1977): the baseline ownership rule
// subject.sub == resource.id grants a non-admin reading its OWN user/roles — there
// is NO Go self-exemption short-circuit (PR-10c's RequirePermissionOrSelf was
// replaced by RequirePermissionForResource, which forwards the resource id to the PDP).
//
// Cases (admin/non-admin tokens + the regular user's own id come from the shared
// provisionPDPAdminAndUser sequence):
//   - admin  GET  /api/v1/access/policies        → 200 (baseline allow, policy:read)
//   - admin  POST /api/v1/access/policies        → NOT 401/403 (baseline allow, policy:write)
//   - non-admin GET /api/v1/access/policies      → 403 + ERR_AUTH_FORBIDDEN + "insufficient
//     permissions" (proves the deny is a PDP decision, not a fail-closed no-Authorizer gap)
//   - non-admin GET /api/v1/access/users/{self}  → 200 (PDP ownership rule subject.sub ==
//     resource.id; the only way a non-admin reaches 200 here, so it proves the rule fired)
//   - non-admin GET /api/v1/access/users/{other} → 403 PDP-deny (not owner, no user:read)
//   - non-admin PATCH /api/v1/access/users/{self}  → 200 (PDP ownership rule on user:write —
//     the userWriteOwnerGate path: update/patch/change-password; proves write ownership, not
//     just read, is PDP-decided — F5/#2025)
//   - non-admin PATCH /api/v1/access/users/{other} → 403 PDP-deny (not owner, no user:write)
//   - admin     GET /api/v1/access/users/{user}  → 200 (baseline allow, user:read)
//   - non-admin GET /api/v1/access/roles/{self}  → 200 (PDP ownership rule)
//   - non-admin GET /api/v1/access/roles/{other} → 403 PDP-deny (not owner, no role:read)
//
// Cross-tenant ownership deny (#2026): a NON-admin subject provisioned in a SECOND
// tenant (testTenantID2) is denied on testTenantID resources by the SAME owner-gate
// ownership rule — tenant is NOT an owner-grant factor (the rule compares request-local
// subject.sub vs resource.id, so a different-tenant subject is denied exactly like a
// same-tenant non-owner). Covers both owner-gates (identitymanage read+write, rbaccheck):
//   - tenant-B non-admin GET   /api/v1/access/users/{tenantA-user}  → 403 PDP ownership deny
//   - tenant-B non-admin GET   /api/v1/access/roles/{tenantA-user}  → 403 PDP ownership deny
//   - tenant-B non-admin PATCH /api/v1/access/users/{tenantA-user}  → 403 PDP ownership deny

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestABACPDPGatesAccesscore(t *testing.T) {
	base := startCorebundlePDPApp(t)
	adminToken, userToken, userID, adminID := provisionPDPAdminAndUser(t, base, testTenantID)

	// crossTenantUserToken + crossTenantUserID belong to a NON-admin user fully provisioned
	// in a SECOND tenant (testTenantID2): the JWT carries sub=crossTenantUserID and
	// tenant_id=tenantB, backed by a live session so it passes JWT+session auth and reaches
	// the PDP. Reading a testTenantID resource with it must 403 — the owner-gate ownership
	// rule (subject.sub == resource.id) compares request-local UUIDs and is tenant-agnostic,
	// so a different-tenant subject is denied exactly like a same-tenant non-owner. The admin
	// token is discarded; only the non-admin user token exercises the ownership rule.
	_, crossTenantUserToken, crossTenantUserID, _ := provisionPDPAdminAndUser(t, base, testTenantID2)

	// --- policymanage: admin allowed (baseline), non-admin denied by PDP ---

	t.Run("admin_list_policies_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/policies", adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET /api/v1/access/policies must be 200 (baseline allow, policy:read); body=%s", body)
	})

	t.Run("admin_create_policy_not_4xx_auth", func(t *testing.T) {
		// Exercise policy:write through the real path. We do not pin the exact success
		// code (the empty body may 400), only that the gate admitted the caller.
		resp, body := pdpAccessReq(t, base, http.MethodPost, "/api/v1/access/policies", adminToken, []byte(`{}`))
		assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode, "admin POST policies must not be 401; body=%s", body)
		assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
			"admin POST policies must not be 403 (baseline allow, policy:write); body=%s", body)
	})

	t.Run("non_admin_list_policies_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/policies", userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET /api/v1/access/policies must be 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (a missing-Authorizer gap); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	// --- identitymanage: self-access via the PDP ownership rule (#1977) ---

	t.Run("non_admin_get_own_user_200_pdp_ownership", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/users/"+userID, userToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"non-admin GET own user (subject==resource.id) must be 200 via the PDP ownership rule "+
				"(a non-admin has no baseline admin grant, so 200 here proves the ownership rule fired); body=%s", body)
	})

	t.Run("non_admin_get_other_user_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/users/"+adminID, userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET another user must be 403 (PDP deny, no user:read); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"non-self deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s", "insufficient permissions", body)
	})

	t.Run("admin_get_user_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/users/"+userID, adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET a user must be 200 (baseline allow, user:read); body=%s", body)
	})

	// --- identitymanage write ownership: PATCH self via the userWriteOwnerGate PDP
	// ownership rule (user:write); non-self write is PDP-denied (#1977, F5/#2025). ---

	t.Run("non_admin_self_patch_user_200_pdp_ownership", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodPatch, "/api/v1/access/users/"+userID, userToken, []byte(`{"name":"Self Updated"}`))
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"non-admin PATCH own user (subject==resource.id) must be 200 via the PDP ownership rule on "+
				"user:write (a non-admin has no baseline user:write grant, so 200 proves the ownership rule "+
				"fired on the write path, not just read); body=%s", body)
	})

	t.Run("non_admin_patch_other_user_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodPatch, "/api/v1/access/users/"+adminID, userToken, []byte(`{"name":"Hijack"}`))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin PATCH another user must be 403 (PDP deny, not owner + no user:write); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"non-self write deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s", "insufficient permissions", body)
	})

	// --- rbaccheck: self-access via PDP ownership + PDP deny for another user's roles ---

	t.Run("non_admin_get_own_roles_200_pdp_ownership", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/roles/"+userID, userToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"non-admin GET own roles (subject==resource.id) must be 200 via the PDP ownership rule; body=%s", body)
	})

	t.Run("non_admin_get_other_roles_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/roles/"+adminID, userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET another user's roles must be 403 (PDP deny, no role:read); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
	})

	// --- cross-tenant ownership deny (#2026): the tenant-B non-admin token is denied on
	// tenant-A resources by the SAME owner-gate ownership rule (subject.sub != resource.id).
	// The deny is identical to the same-tenant non-owner cases above; only the subject's
	// tenant_id claim differs — proving tenant is never an owner-grant factor. Covers the
	// identitymanage read+write gates and the rbaccheck read gate. ---

	// Positive control: the cross-tenant token is LIVE and ownership works in its OWN tenant —
	// a tenant-B non-admin reading its OWN tenant-B user is 200 (subject.sub == resource.id in
	// tenant-B). This proves the 403s below are targeted ownership denials, not a dead/invalid
	// token surfacing as a blanket auth failure.
	t.Run("cross_tenant_self_access_own_tenant_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/users/"+crossTenantUserID, crossTenantUserToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"tenant-B non-admin GET its OWN tenant-B user must be 200 (PDP ownership rule fires in its own "+
				"tenant; proves the cross-tenant token is live, so the 403s below are ownership-specific); body=%s", body)
	})

	t.Run("cross_tenant_get_other_user_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/users/"+userID, crossTenantUserToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tenant-B non-admin GET a tenant-A user must be 403 (PDP ownership rule subject.sub != "+
				"resource.id; the JWT's tenant_id differs but the rule is tenant-agnostic); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "cross-tenant deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"cross-tenant deny must be a PDP ownership decision (%q), not a no-Authorizer gap; body=%s",
			"insufficient permissions", body)
	})

	t.Run("cross_tenant_get_other_roles_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, "/api/v1/access/roles/"+userID, crossTenantUserToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tenant-B non-admin GET a tenant-A user's roles must be 403 (PDP ownership rule, not owner + no role:read); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "cross-tenant deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
	})

	t.Run("cross_tenant_patch_other_user_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodPatch, "/api/v1/access/users/"+userID, crossTenantUserToken, []byte(`{"name":"X-Tenant Hijack"}`))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tenant-B non-admin PATCH a tenant-A user must be 403 (PDP ownership rule on user:write, not owner); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "cross-tenant write deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"cross-tenant write deny must be a PDP ownership decision (%q), not a no-Authorizer gap; body=%s",
			"insufficient permissions", body)
	})
}

// pdpAccessReq issues an authenticated request to an accesscore endpoint with the
// given Bearer token (the tenant claim travels inside the JWT, mirroring
// pdpAuditReq). Returns the response and the already-read, already-closed body.
func pdpAccessReq(t *testing.T, base, method, path, token string, body []byte) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	return resp, string(raw)
}
