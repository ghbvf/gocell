package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// devicestate contract (issue #1899, epic #1895 PR-3): a GET presence query
// (online/offline/last-seen) that lets MDM/ZT consume any device provider.
// ownerCell:_framework + lifecycle:draft (ADR 202606130635-1939).
//
// observedAt is a required freshness anchor (always present, even for unknown);
// lastSeenAt (last actual contact) stays optional. tenantId is removed as a
// client-passed query param (framework-derived from the principal) but remains
// a framework-produced response field for multi-tenant routing.
//
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestDeviceState_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.devicestate.v1")

	// normal response (observedAt required; tenantId framework-produced)
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","state":"online","observedAt":"2026-06-13T00:00:00Z",
		"lastSeenAt":"2026-06-13T00:00:00Z","tenantId":"t-1"
	}}`))
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"offline","observedAt":"2026-06-13T00:00:00Z"}}`))
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"unknown","observedAt":"2026-06-13T00:00:00Z"}}`))

	// parameter errors
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"bogus","observedAt":"2026-06-13T00:00:00Z"}}`)) // bad enum
	c.MustRejectResponse(t, []byte(`{"data":{"state":"online","observedAt":"2026-06-13T00:00:00Z"}}`))                   // missing deviceId
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"online"}}`))                                    // missing observedAt

	// query param validation (FMT-25 maxLength)
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	// auth-boundary declaration (every declared status conforms)
	c.ValidateErrorResponse(t, 400, []byte(`{"error":{"code":"ERR_VALIDATION","message":"bad request","details":[]}}`))
	c.ValidateErrorResponse(t, 401, []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"forbidden","details":[]}}`))
	c.ValidateErrorResponse(t, 404, []byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found","details":[]}}`))
}
