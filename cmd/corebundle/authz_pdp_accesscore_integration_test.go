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
//   - admin     GET /api/v1/access/users/{user}  → 200 (baseline allow, user:read)
//   - non-admin GET /api/v1/access/roles/{self}  → 200 (PDP ownership rule)
//   - non-admin GET /api/v1/access/roles/{other} → 403 PDP-deny (not owner, no role:read)

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
	adminToken, userToken, userID, adminID := provisionPDPAdminAndUser(t, base)

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
