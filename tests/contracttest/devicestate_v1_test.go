package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// devicestate contract (issue #1899, epic #1895 PR-3): a GET presence query
// (online/offline/last-seen) that lets MDM/ZT consume any device provider.
// ownerCell:_framework + lifecycle:active (draft→active in #2037 PR-8a; the
// deviceId moved query→path `/api/v1/devicestate/{id}` for per-device ABAC
// ownership in #2348 F3). ADR 202606130635-1939.
//
// observedAt is a required freshness anchor (always present, even for unknown).
// lastSeenAt (last actual contact) and tenantId are now also required (full-column-set
// schema-truth-source closure, #2359): every projection column key is always present on
// the wire. lastSeenAt is nullable (`["string","null"]`) — its schema-valid "no value"
// is JSON null (device never seen); tenantId is a framework-produced (not client-passed)
// required string for multi-tenant routing.
//
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestDeviceState_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.devicestate.v1")

	// normal response — full column set: every key present. lastSeenAt carries a real
	// timestamp (seen) or null (never seen); tenantId framework-produced.
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","state":"online","observedAt":"2026-06-13T00:00:00Z",
		"lastSeenAt":"2026-06-13T00:00:00Z","tenantId":"t-1"
	}}`))
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"offline","observedAt":"2026-06-13T00:00:00Z","lastSeenAt":"2026-06-12T00:00:00Z","tenantId":"t-1"}}`))
	// unknown/never-seen: lastSeenAt is the schema-valid null (key present, no value).
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"unknown","observedAt":"2026-06-13T00:00:00Z","lastSeenAt":null,"tenantId":"t-1"}}`))

	// parameter errors — each isolates ONE violation (other required cols present).
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"bogus","observedAt":"2026-06-13T00:00:00Z","lastSeenAt":null,"tenantId":"t-1"}}`)) // bad enum
	c.MustRejectResponse(t, []byte(`{"data":{"state":"online","observedAt":"2026-06-13T00:00:00Z","lastSeenAt":null,"tenantId":"t-1"}}`))                   // missing deviceId
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"online","lastSeenAt":null,"tenantId":"t-1"}}`))                                    // missing observedAt
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"online","observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1"}}`))                  // missing lastSeenAt (now required)
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"online","observedAt":"2026-06-13T00:00:00Z","lastSeenAt":null}}`))                 // missing tenantId (now required)

	// path param validation (FMT-25 minLength/maxLength). deviceId is the path
	// param `id` (GET /api/v1/devicestate/{id}); it enters the PDP as the ABAC
	// resource via RequirePermissionForResource("id", device:read) (#2348 F3).
	c.ValidatePathParam(t, "id", "dev-1")
	c.MustRejectPathParam(t, "id", "")                       // minLength 1
	c.MustRejectPathParam(t, "id", strings.Repeat("x", 257)) // maxLength 256

	// auth-boundary declaration (every declared status conforms)
	c.ValidateErrorResponse(t, 400, []byte(`{"error":{"code":"ERR_VALIDATION","message":"bad request","details":[]}}`))
	c.ValidateErrorResponse(t, 401, []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"forbidden","details":[]}}`))
	c.ValidateErrorResponse(t, 404, []byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found","details":[]}}`))
}
