//go:build integration

package main

// TestABACPDPGatesConfigcore is the PR-10b T10.x acceptance test:
// the wired ABAC PDP must gate real HTTP traffic on configcore read, write,
// flag read, flag write, and config publish endpoints.
//
// Cases:
//   - admin GET  /api/v1/config/ → 200 (baseline allow via PDP, config:read)
//   - admin POST /api/v1/config/ → 201 (baseline allow via PDP, config:write)
//   - non-admin GET  /api/v1/config/ → 403, body contains ERR_AUTH_FORBIDDEN
//     AND "insufficient permissions" (proves deny comes from PDP, not a missing
//     Authorizer wiring that would return "authorization policy engine not wired")
//   - non-admin POST /api/v1/config/ → 403 + same PDP-deny assertion
//   - admin GET  /api/v1/flags/ → 200 (baseline allow via PDP, flag:read)
//   - non-admin GET  /api/v1/flags/ → 403 + PDP-deny assertion (flag:read)
//   - admin POST /api/v1/flags/ → 201 (baseline allow via PDP, flag:write)
//   - non-admin POST /api/v1/flags/ → 403 + PDP-deny assertion (flag:write)
//   - admin POST /api/v1/config/{key}/publish → NOT 401 and NOT 403 (gate
//     passes; business result may be 404 if key absent, not pinned)
//   - non-admin POST /api/v1/config/{key}/publish → 403 + PDP-deny assertion
//
// configcore has no self branch (unlike auditquery), so only admin-allow and
// non-admin-deny cases exist.
//
// Wiring: same composition root path as TestABACPDPGatesAuditQuery — uses the
// startCorebundlePDPApp + provisionPDPAdminAndUser helpers from
// authz_pdp_integration_test.go.

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pdpConfigListPath is the config list endpoint served by the configread slice
// (generated from the http.config.list.v1 contract).
const pdpConfigListPath = "/api/v1/config/"

// pdpFlagListPath is the flag list endpoint served by the featureflag slice
// (generated from the http.config.flags.list.v1 contract).
const pdpFlagListPath = "/api/v1/flags/"

// pdpConfigPublishPath returns the publish endpoint for the given config key
// (generated from the http.config.publish.v1 contract).
func pdpConfigPublishPath(key string) string { return "/api/v1/config/" + key + "/publish" }

// pdpConfigReq issues an HTTP request to a configcore endpoint with the given
// method, path, token, and optional JSON body. Returns the response and body
// string. Body is already closed.
func pdpConfigReq(t *testing.T, base, method, path, token string, body []byte) (*http.Response, string) {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, base+path, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", testTenantID)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := pdpAuditClient.Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	return resp, string(raw)
}

func TestABACPDPGatesConfigcore(t *testing.T) {
	base := startCorebundlePDPApp(t)
	adminToken, userToken, _, _ := provisionPDPAdminAndUser(t, base, testTenantID)

	writeBody := []byte(`{"key":"e2e.k","value":"v"}`)

	// Case 1: admin GET /api/v1/config/ → 200 (baseline allow, config:read).
	t.Run("admin_list_200", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodGet, pdpConfigListPath, adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET /api/v1/config/ must get 200 (baseline allow via PDP); body=%s", body)
	})

	// Case 2: admin POST /api/v1/config/ → 201 (baseline allow, config:write).
	t.Run("admin_write_201", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpConfigListPath, adminToken, writeBody)
		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"admin POST /api/v1/config/ must get 201 (baseline allow via PDP); body=%s", body)
	})

	// Case 3: non-admin GET /api/v1/config/ → PDP default-deny → 403.
	// F7: assert on "insufficient permissions" to prove the deny came from the
	// PDP path, not a fail-closed "authorization policy engine not wired" gap.
	t.Run("non_admin_list_403", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodGet, pdpConfigListPath, userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET /api/v1/config/ must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	// Case 4: non-admin POST /api/v1/config/ → PDP default-deny → 403.
	// Same PDP-deny assertion as Case 3 for the write path.
	t.Run("non_admin_write_403", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpConfigListPath, userToken, writeBody)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin POST /api/v1/config/ must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	// Case 5: admin GET /api/v1/flags/ → 200 (baseline allow via PDP, flag:read).
	t.Run("admin_flag_list_200", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodGet, pdpFlagListPath, adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET /api/v1/flags/ must get 200 (baseline allow via PDP); body=%s", body)
	})

	// Case 6: non-admin GET /api/v1/flags/ → PDP default-deny → 403 (flag:read).
	// F7: assert on "insufficient permissions" to prove the deny came from the
	// PDP path, not a fail-closed no-Authorizer 403.
	t.Run("non_admin_flag_list_403", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodGet, pdpFlagListPath, userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET /api/v1/flags/ must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	flagBody := []byte(`{"key":"e2e.flag","enabled":false,"rolloutPercentage":0,"description":"d"}`)

	// Case 7: admin POST /api/v1/flags/ → 201 (baseline allow via PDP, flag:write).
	t.Run("admin_flag_write_201", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpFlagListPath, adminToken, flagBody)
		assert.Equal(t, http.StatusCreated, resp.StatusCode,
			"admin POST /api/v1/flags/ must get 201 (baseline allow via PDP); body=%s", body)
	})

	// Case 8: non-admin POST /api/v1/flags/ → PDP default-deny → 403 (flag:write).
	// F7: assert on "insufficient permissions" to prove the deny came from the
	// PDP path.
	t.Run("non_admin_flag_write_403", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpFlagListPath, userToken, flagBody)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin POST /api/v1/flags/ must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})

	// Case 9: admin POST /api/v1/config/{key}/publish → gate passes (NOT 401,
	// NOT 403). Business result may be 404 if key is absent; we do not pin the
	// exact 2xx because the key "e2e.pubkey" may not exist in the in-memory
	// store. The critical assertion is that the PDP allowed the request through.
	t.Run("admin_publish_gate_pass", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpConfigPublishPath("e2e.pubkey"), adminToken, nil)
		assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
			"admin POST publish must not get 401; body=%s", body)
		assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
			"admin POST publish must not get 403 (PDP must allow via config:publish); body=%s", body)
	})

	// Case 10: non-admin POST /api/v1/config/{key}/publish → PDP default-deny → 403
	// (config:publish). F7: assert on "insufficient permissions" to prove the
	// deny came from the PDP path.
	t.Run("non_admin_publish_403", func(t *testing.T) {
		resp, body := pdpConfigReq(t, base, http.MethodPost, pdpConfigPublishPath("e2e.pubkey"), userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin POST publish must get 403 (PDP default-deny); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN",
			"PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"PDP deny message must be %q, not %q (which would indicate a missing Authorizer wiring); body=%s",
			"insufficient permissions", "authorization policy engine not wired", body)
	})
}
