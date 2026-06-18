package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// devicecompliance contract (issue #1900, epic #1895 PR-4): a GET posture query
// (disk-encryption / antivirus / patch / firewall + overall verdict) that feeds
// Authorize (Zero-Trust) decisions, letting MDM/ZT consume any device provider
// (winmdm/Intune/Jamf/自建). ownerCell:_framework + lifecycle:draft (ADR
// 202606130635-1939); serving is deferred to the future MDM/ZT work, so there is
// no slice and no handler to exercise — the contract-level surface testable today
// is the JSON-schema wire shape and the declared error-response (auth boundary)
// envelope.
//
// Posture attributes are provider-neutral (diskEncryption, not BitLocker) and
// REQUIRED: a compliance reading always states every attribute, with "unknown"
// the explicit not-reported value — an absent attribute is ambiguous for a
// security (Authorize) decision, and an absent optional enum would marshal to ""
// through the responseProjection ToMap path (codex F1), which is not a valid enum
// member. Making them required + unknown closes that schema-invalid-wire gap.
// observedAt is a required freshness anchor. tenantId is now also required
// (full-column-set schema-truth-source closure, #2359): the projection column key is
// always present on the wire. It is framework-produced (never a client param); a
// single-tenant deployment still emits it (its tenant id), not an absent key.
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

	// normal response — full posture + framework-produced tenantId
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"enabled","antivirus":"enabled","patch":"upToDate","firewall":"enabled",
		"tenantId":"t-1"
	}}`))
	// not-reported posture → explicit "unknown" (required, never absent)
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":false,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"unknown",
		"tenantId":"t-1"
	}}`))
	// mixed posture
	c.ValidateResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":false,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"disabled","antivirus":"enabled","patch":"outOfDate","firewall":"unknown",
		"tenantId":"t-1"
	}}`))

	// parameter errors — bad value on each posture attribute (closed enum; full set, one bad)
	for _, posture := range []string{
		`"diskEncryption":"bogus","antivirus":"unknown","patch":"unknown","firewall":"unknown"`,
		`"diskEncryption":"unknown","antivirus":"bogus","patch":"unknown","firewall":"unknown"`,
		`"diskEncryption":"unknown","antivirus":"unknown","patch":"bogus","firewall":"unknown"`,
		`"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"bogus"`,
	} {
		c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1",`+posture+`}}`))
	}
	// regression (codex F1): empty string is NOT a valid enum — required posture closes the
	// optional-enum + ToMap-ignores-omitempty schema-invalid-wire gap.
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1",`+
		`"diskEncryption":"","antivirus":"unknown","patch":"unknown","firewall":"unknown"}}`))
	// missing a required posture attribute (each posture field is now required)
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1",`+
		`"antivirus":"unknown","patch":"unknown","firewall":"unknown"}}`)) // missing diskEncryption

	// parameter errors — wrong type + missing base required + missing data
	// (full posture so the rejection isolates the base-field problem)
	c.MustRejectResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":"yes","observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1",
		"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"unknown"
	}}`)) // compliant must be boolean
	c.MustRejectResponse(t, []byte(`{"data":{
		"compliant":true,"observedAt":"2026-06-13T00:00:00Z","tenantId":"t-1",
		"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"unknown"
	}}`)) // missing deviceId
	c.MustRejectResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":true,"tenantId":"t-1",
		"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"unknown"
	}}`)) // missing observedAt
	c.MustRejectResponse(t, []byte(`{"data":{
		"deviceId":"dev-1","compliant":true,"observedAt":"2026-06-13T00:00:00Z",
		"diskEncryption":"unknown","antivirus":"unknown","patch":"unknown","firewall":"unknown"
	}}`)) // missing tenantId (now required, #2359)
	c.MustRejectResponse(t, []byte(`{}`)) // missing data (top-level required)

	// query param validation (FMT-25 maxLength)
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	// auth-boundary declaration (every declared status conforms)
	c.ValidateErrorResponse(t, 400, []byte(`{"error":{"code":"ERR_VALIDATION","message":"bad request","details":[]}}`))
	c.ValidateErrorResponse(t, 401, []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"forbidden","details":[]}}`))
	c.ValidateErrorResponse(t, 404, []byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found","details":[]}}`))
}
