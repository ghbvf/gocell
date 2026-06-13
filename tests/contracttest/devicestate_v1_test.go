package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// devicestate contract (issue #1899, epic #1895 PR-3): a GET presence query
// (online/offline/last-seen) that lets MDM/ZT consume any device provider.
// ownerCell:_framework + lifecycle:draft (ADR 202606130635-1939).
//
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestDeviceState_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.devicestate.v1")

	// normal response
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"online","lastSeenAt":"2026-06-13T00:00:00Z","tenantId":"t-1"}}`))
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"offline"}}`))
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"unknown"}}`))

	// parameter errors
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","state":"bogus"}}`)) // bad deviceState enum
	c.MustRejectResponse(t, []byte(`{"data":{"state":"online"}}`))                   // missing deviceId

	// query param validation (FMT-25 maxLength)
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	// auth-boundary declaration
	c.ValidateErrorResponse(t, 401, []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"forbidden","details":[]}}`))
}
