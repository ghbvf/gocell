//go:build integration

package main

// TestABACPDPGatesDevicestate is the device-ownership acceptance test for #2351 (+ #2400
// review F1): the wired ABAC PDP must gate real HTTP traffic on the framework-owned
// GET /api/v1/devicestate/{id} serving handler via the device:read baseline rules
// (cellmodules/deviceserving + corecells/accesscore baseline). Before #2351 there was no
// device:read baseline rule, so the endpoint fail-closed to deny on every authenticated
// request; this test proves the admin grant fires and that non-owner / cross-tenant / and —
// critically (#2400 F1) — non-device-kind subjects are denied.
//
// device:read self ownership is KIND-GATED (#2400 F1): the self rule is
// subject.kind == device AND subject.sub == resource.id, so ONLY a device principal reads
// its own state. The provisioning helper mints JWT USER principals (no device-cert path in
// this harness), so this integration test cannot exercise the positive device-self 200 — that
// is unit-covered (corecells .../baseline_test.go TestBuiltinBaseline_DeviceRead with a device
// principal, and cellmodules/deviceserving/service_test.go TestDevicestate_PerDeviceOwnership).
// What this test proves end-to-end against the REAL baseline:
//   - admin GET /api/v1/devicestate/{anyID} → 200 (admin rule, kind-agnostic)
//   - non-admin USER GET its OWN id → 403 (#2400 F1: kind != device, so id-match alone fails)
//   - non-admin USER GET another id → 403 (not owner, no admin)
//   - cross-tenant admin GET tenant-A id → 200 (admin rule is tenant-agnostic at the ROUTE gate)
//   - cross-tenant USER GET tenant-A id → 403 (not device, not owner, not admin)
//
// The honest "unknown" body on the 200s confirms no presence data is exposed (no backend yet).
// The 403s asserting ERR_AUTH_FORBIDDEN + "insufficient permissions" (not 401) prove the deny
// is a PDP decision on a LIVE, authenticated token — not a dead token or unwired Authorizer.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestABACPDPGatesDevicestate(t *testing.T) {
	base := startCorebundlePDPApp(t)
	adminToken, userToken, userID, _ := provisionPDPAdminAndUser(t, base, testTenantID)

	// A non-admin user (+ an admin) fully provisioned in a SECOND tenant (testTenantID2):
	// the tenant-B admin pins the admin-cross-tenant ROUTE behavior; the tenant-B non-admin
	// proves a cross-tenant non-device subject is denied (its 403 carrying "insufficient
	// permissions" — not 401 — also confirms the token is live and reached the PDP).
	crossTenantAdminToken, crossTenantUserToken, _, _ := provisionPDPAdminAndUser(t, base, testTenantID2)

	devicePath := func(id string) string { return "/api/v1/devicestate/" + id }

	t.Run("admin_read_any_device_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET any devicestate must be 200 (baseline admin rule, device:read); body=%s", body)
		assert.Contains(t, body, `"state":"unknown"`,
			"the 200 body must carry the honest unknown state (no presence backend); body=%s", body)
	})

	// #2400 F1 regression guard at the integration level: a normal USER whose subject UUID
	// equals the requested device id is DENIED, because the device-self rule requires
	// subject.kind == device. A coincidental id match by a non-device principal must NOT grant
	// device-self read. (A real device principal reading its own id IS allowed — unit-covered.)
	t.Run("non_admin_user_read_own_id_403_not_device_kind", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin USER GET its own id must be 403 (#2400 F1: device-self requires kind==device, "+
				"so a user whose subject==resource.id does NOT get device-self read); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s", "insufficient permissions", body)
	})

	t.Run("non_admin_read_other_device_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET another device's state must be 403 (PDP deny, not owner, not device, no admin); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"non-owner deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s", "insufficient permissions", body)
	})

	// admin cross-tenant is allowed AT THE ROUTE GATE by design: the admin baseline rule
	// (adminOrSuperAdmin()) is tenant-AGNOSTIC, exactly like every other admin baseline
	// (user:read, audit:read, …). The actual cross-tenant data boundary for admins is the
	// data layer (principal-derived RowScope=tenant + RLS), which lands with the presence
	// backend (#1904/#1905) — there is no presence data to isolate yet, so the handler
	// returns the same honest "unknown" for any id. This case pins that route-level behavior
	// so a future change that tries to make the admin rule tenant-scoped (which would belong
	// in the data layer, not the route gate) is a conscious, test-visible decision.
	t.Run("cross_tenant_admin_read_other_tenant_device_200_route_gate", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), crossTenantAdminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"tenant-B admin GET a tenant-A device id passes the ROUTE gate (admin baseline is tenant-agnostic, "+
				"by design — data-layer RowScope=tenant is the real boundary, deferred to the presence backend); body=%s", body)
		assert.Contains(t, body, `"state":"unknown"`,
			"the 200 body must carry the honest unknown state (no presence backend → no cross-tenant data exposed); body=%s", body)
	})

	t.Run("cross_tenant_user_read_other_device_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), crossTenantUserToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tenant-B non-admin GET a tenant-A device id must be 403 (not device, not owner, not admin; the "+
				"JWT's tenant_id differs but the rules are tenant-agnostic); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "cross-tenant deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"cross-tenant deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s",
			"insufficient permissions", body)
	})
}
