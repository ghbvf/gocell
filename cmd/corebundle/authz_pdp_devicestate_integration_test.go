//go:build integration

package main

// TestABACPDPGatesDevicestate is the device-ownership acceptance test for #2351:
// the wired ABAC PDP must gate real HTTP traffic on the framework-owned
// GET /api/v1/devicestate/{id} serving handler via the device:read baseline rules
// (cellmodules/deviceserving + corecells/accesscore baseline). Before #2351 there
// was no device:read baseline rule, so the endpoint fail-closed to deny on every
// authenticated request; this test proves the new device-SELF ownership grant
// (subject.sub == resource.id) and the admin grant fire, and that a non-owner /
// cross-tenant subject is denied.
//
// device:read uses device-SELF ownership (subject == resource.id) — shape-identical
// to user:read-self. The baseline rule is KIND-AGNOSTIC: it compares the JWT subject
// against the canonical path-param id, so a non-admin user reading
// /api/v1/devicestate/{own-sub} exercises exactly the same rule a real device
// principal hits when reading its own id (the device-principal path is unit-covered
// by cellmodules/deviceserving/service_test.go TestDevicestate_PerDeviceOwnership).
// The handler always returns state "unknown" (no presence backend yet), so a 200
// here proves the gate admitted the caller, not that real presence data leaked.
//
// Cases (mirrors TestABACPDPGatesAccesscore's ownership shape):
//   - admin     GET /api/v1/devicestate/{anyID}   → 200 (baseline admin rule, device:read)
//   - non-admin GET /api/v1/devicestate/{self}    → 200 (PDP ownership rule subject.sub == resource.id)
//   - non-admin GET /api/v1/devicestate/{other}   → 403 + ERR_AUTH_FORBIDDEN + "insufficient permissions"
//   - cross-tenant non-admin GET its OWN id       → 200 (positive control: token is live)
//   - cross-tenant non-admin GET tenant-A id      → 403 (ownership rule is tenant-agnostic, #2026)

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestABACPDPGatesDevicestate(t *testing.T) {
	base := startCorebundlePDPApp(t)
	adminToken, userToken, userID, _ := provisionPDPAdminAndUser(t, base, testTenantID)

	// A non-admin user fully provisioned in a SECOND tenant (testTenantID2): its JWT
	// carries sub=crossTenantUserID and tenant_id=tenantB, backed by a live session.
	// Reading a tenant-A device id with it must 403 — the ownership rule compares
	// request-local UUIDs and is tenant-agnostic, so a different-tenant subject is
	// denied exactly like a same-tenant non-owner.
	_, crossTenantUserToken, crossTenantUserID, _ := provisionPDPAdminAndUser(t, base, testTenantID2)

	devicePath := func(id string) string { return "/api/v1/devicestate/" + id }

	t.Run("admin_read_any_device_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), adminToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"admin GET any devicestate must be 200 (baseline admin rule, device:read); body=%s", body)
	})

	t.Run("non_admin_read_own_device_200_pdp_ownership", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), userToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"non-admin GET own device state (subject==resource.id) must be 200 via the PDP ownership rule "+
				"(a non-admin has no baseline admin grant, so 200 here proves the device-self rule fired); body=%s", body)
	})

	t.Run("non_admin_read_other_device_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), userToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"non-admin GET another device's state must be 403 (PDP deny, not owner, no device:read); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "PDP deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"non-owner deny must be a PDP decision (%q), not a no-Authorizer gap; body=%s", "insufficient permissions", body)
	})

	// Positive control: the cross-tenant token is LIVE and ownership works in its OWN
	// tenant (subject.sub == resource.id in tenant-B), so the 403 below is an
	// ownership-specific denial, not a dead/invalid token surfacing as a blanket failure.
	t.Run("cross_tenant_read_own_device_own_tenant_200", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(crossTenantUserID), crossTenantUserToken, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"tenant-B non-admin GET its OWN id must be 200 (ownership rule fires in its own tenant; proves the "+
				"cross-tenant token is live, so the 403 below is ownership-specific); body=%s", body)
	})

	t.Run("cross_tenant_read_other_device_403_pdp_deny", func(t *testing.T) {
		resp, body := pdpAccessReq(t, base, http.MethodGet, devicePath(userID), crossTenantUserToken, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tenant-B non-admin GET a tenant-A device id must be 403 (ownership rule subject.sub != resource.id; "+
				"the JWT's tenant_id differs but the rule is tenant-agnostic); body=%s", body)
		assert.Contains(t, body, "ERR_AUTH_FORBIDDEN", "cross-tenant deny must produce ERR_AUTH_FORBIDDEN; body=%s", body)
		assert.Contains(t, body, "insufficient permissions",
			"cross-tenant deny must be a PDP ownership decision (%q), not a no-Authorizer gap; body=%s",
			"insufficient permissions", body)
	})
}
