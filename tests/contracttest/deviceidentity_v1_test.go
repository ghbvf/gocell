package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// deviceidentity contracts (issue #1899, epic #1895 PR-3). These contracts are
// ownerCell:_framework + lifecycle:draft (ADR 202606130635-1939); serving is
// deferred to PR-7/PR-8, so there is no slice and no handler to exercise — the
// contract-level surface that IS testable today is the JSON-schema wire shape
// and the declared error-response (auth boundary) envelope. Coverage per T3.1:
// normal schema / parameter errors / auth-boundary declaration / path·query.
//
// ref: docs/architecture/202606130635-1939-adr-framework-owned-contract.md
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// validErrorBody is the shared error-response envelope shape; reused for every
// declared 4xx auth-boundary assertion (requestId is framework-injected and not
// required by the schema).
var validErrorBody = []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`)

func TestDeviceIdentityEnroll_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.enroll.v1")

	// normal schema
	c.ValidateRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1"}`))
	c.ValidateRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1","tenantId":"t-1","requestedDuration":"2160h","usages":["client auth","digital signature"],"subjectAltNames":{"dnsNames":["dev1.example.com"]}}`))
	c.ValidateResponse(t, []byte(`{"data":{"certificate":"Y2VydA==","chain":"Y2hhaW4=","serial":"01AB","notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","epoch":0,"status":"issued","deviceId":"dev-1"}}`))

	// parameter errors
	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1"}`))                              // missing csr
	c.MustRejectRequest(t, []byte(`{"csr":"Q1NSREVS"}`))                                // missing deviceId
	// NOTE: keyUsage allow-set is NOT enforced at the wire layer — contractgen cannot
	// generate typed enums for array items, and the allowed-usage set is the Signer's
	// SignConstraints responsibility (spec FR-005, PR-5). usages is a free-form string array here.
	c.MustRejectResponse(t, []byte(`{"data":{"certificate":"Y2VydA==","chain":"Y2hhaW4=","serial":"01AB","notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","epoch":0,"status":"bogus"}}`)) // bad certStatus enum
	c.MustRejectResponse(t, []byte(`{"data":{"serial":"01AB","status":"issued"}}`))     // missing required cert material

	// auth-boundary declaration (401/403 declared + body conforms)
	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 403, validErrorBody)
}

func TestDeviceIdentityRenew_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.renew.v1")

	c.ValidateRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1","priorSerial":"01AB"}`))
	c.ValidateResponse(t, []byte(`{"data":{"certificate":"Y2VydA==","chain":"Y2hhaW4=","serial":"02CD","notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","epoch":1,"status":"rotated","deviceId":"dev-1","priorSerial":"01AB"}}`))

	c.MustRejectRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1"}`)) // missing priorSerial
	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1","priorSerial":"01AB"}`)) // missing csr

	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 404, validErrorBody)
}

func TestDeviceIdentityRevoke_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.revoke.v1")

	c.ValidateRequest(t, []byte(`{"serial":"01AB","deviceId":"dev-1","reason":"keyCompromise"}`))
	c.ValidateRequest(t, []byte(`{"serial":"01AB","deviceId":"dev-1","tenantId":"t-1","issuer":"softca","reason":"superseded"}`))
	c.ValidateResponse(t, []byte(`{"data":{"serial":"01AB","status":"revoked","reason":"keyCompromise","revokedAt":"2026-06-13T00:00:00Z"}}`))

	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1","reason":"keyCompromise"}`)) // missing serial
	c.MustRejectRequest(t, []byte(`{"serial":"01AB","deviceId":"dev-1"}`))          // missing reason
	c.MustRejectRequest(t, []byte(`{"serial":"01AB","deviceId":"dev-1","reason":"bogus"}`)) // bad revocationReason enum

	c.ValidateErrorResponse(t, 403, validErrorBody) // cross-scope fail-closed
	c.ValidateErrorResponse(t, 404, validErrorBody)
}

func TestDeviceIdentityStatus_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.status.v1")

	// normal response
	c.ValidateResponse(t, []byte(`{"data":{"deviceId":"dev-1","serial":"01AB","status":"active","notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","epoch":0}}`))
	c.MustRejectResponse(t, []byte(`{"data":{"deviceId":"dev-1","serial":"01AB","status":"bogus","notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","epoch":0}}`))

	// query param validation (FMT-25 maxLength)
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.ValidateQueryParam(t, "serial", "01AB")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 404, validErrorBody)
}

func TestDeviceIdentityCertIssuedEvent_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "event.deviceidentity.cert-issued.v1")

	c.ValidatePayload(t, []byte(`{"deviceId":"dev-1","serial":"01AB","epoch":0,"notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","action":"enrolled","actorId":"act-1"}`))
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-abc"}`))

	c.MustRejectPayload(t, []byte(`{"deviceId":"dev-1","epoch":0,"notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","action":"enrolled","actorId":"act-1"}`)) // missing serial
	c.MustRejectPayload(t, []byte(`{"deviceId":"dev-1","serial":"01AB","epoch":0,"notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","action":"bogus","actorId":"act-1"}`)) // bad action enum
	c.MustRejectPayload(t, []byte(`{"deviceId":"dev-1","serial":"01AB","epoch":0,"notBefore":"2026-06-13T00:00:00Z","notAfter":"2027-06-13T00:00:00Z","action":"enrolled","actorId":"act-1","certificate":"leak"}`)) // unevaluated field (no key/cert leak)
}

func TestDeviceIdentityCertRevokedEvent_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "event.deviceidentity.cert-revoked.v1")

	c.ValidatePayload(t, []byte(`{"deviceId":"dev-1","serial":"01AB","reason":"keyCompromise","revokedAt":"2026-06-13T00:00:00Z","actorId":"act-1"}`))
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-def"}`))

	c.MustRejectPayload(t, []byte(`{"deviceId":"dev-1","reason":"keyCompromise","revokedAt":"2026-06-13T00:00:00Z","actorId":"act-1"}`)) // missing serial
	c.MustRejectPayload(t, []byte(`{"deviceId":"dev-1","serial":"01AB","reason":"bogus","revokedAt":"2026-06-13T00:00:00Z","actorId":"act-1"}`)) // bad reason enum
}
