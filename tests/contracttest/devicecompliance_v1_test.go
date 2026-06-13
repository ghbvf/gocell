package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// devicecompliance contract (issue #1900, epic #1895 PR-4): a GET posture query
// (disk-encryption / antivirus / patch / firewall + overall verdict) that feeds
// Authorize (Zero-Trust) decisions, letting MDM/ZT consume any device provider
// (winmdm/Intune/Jamf/自建). ownerCell:_framework + lifecycle:draft (ADR
// 202606130635-1939); serving is deferred to the future MDM/ZT work, so there is
// no slice and no handler to exercise — the contract-level surface testable today
// is the JSON-schema wire shape and the declared error-response (auth boundary)
// envelope. Coverage per T4.1: normal schema / parameter errors / auth-boundary
// declaration / query.
//
// Posture attributes are provider-neutral (diskEncryption, not BitLocker): the
// same shape describes BitLocker/FileVault/LUKS. observedAt is a required
// freshness anchor (a posture verdict without a timestamp is unsafe to trust).
// tenantId is a framework-produced response field (not a client-passed query
// param; tenant is framework-derived from the principal), mirroring devicestate.
//
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013
// ref: docs/architecture/202606130635-1939-adr-framework-owned-contract.md

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestDeviceCompliance_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.devicecompliance.v1")

	// normal response (full posture; tenantId framework-produced)
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"enabled","antivirus":"enabled","patch":"upToDate","firewall":"enabled",
		"tenantId":"t-1"
	}}`))
	// minimal required only (posture attributes optional, may be unknown/absent)
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":false,"observedAt":"2026-06-13T00:00:00Z"}}`))
	// unknown posture values (closed enum third member)
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":false,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"unknown","antivirus":"disabled","patch":"outOfDate","firewall":"unknown"
	}}`))

	// parameter errors — bad value on each posture attribute (closed enum value-set)
	for _, badAttr := range []string{
		`"diskEncryption":"bogus"`,
		`"antivirus":"bogus"`,
		`"patch":"bogus"`,
		`"firewall":"bogus"`,
	} {
		c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z",`+badAttr+`}}`))
	}
	// parameter errors — wrong type + missing required field
	// compliant must be boolean, not string:
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":"yes","observedAt":"2026-06-13T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{"compliant":true,"observedAt":"2026-06-13T00:00:00Z"}}`))   // missing deviceId
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","observedAt":"2026-06-13T00:00:00Z"}}`)) // missing compliant
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":true}}`))                    // missing observedAt

	// query param validation (FMT-25 maxLength)
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	// auth-boundary declaration (every declared status conforms)
	c.ValidateErrorResponse(t, 400, []byte(`{"error":{"code":"ERR_VALIDATION","message":"bad request","details":[]}}`))
	c.ValidateErrorResponse(t, 401, []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"forbidden","details":[]}}`))
	c.ValidateErrorResponse(t, 404, []byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found","details":[]}}`))
}
