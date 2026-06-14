package contracttest_test

// Contract-level TDD coverage for the framework-owned, provider-neutral
// deviceidentity contracts (issue #1899, epic #1895 PR-3). These contracts are
// ownerCell:_framework + lifecycle:draft (ADR 202606130635-1939); serving is
// deferred to PR-7/PR-8, so there is no slice and no handler to exercise — the
// contract-level surface that IS testable today is the JSON-schema wire shape
// and the declared error-response (auth boundary) envelope. Coverage per T3.1:
// normal schema / parameter errors / auth-boundary declaration / path·query.
//
// Certificate identity is provider-neutral issuer+serial (RFC 5280): a shared
// certRef object ($ref contracts/shared/deviceidentity/v1) replaces the old
// flat serial, and the serial carries a hex pattern. Client-passed tenantId is
// removed (tenant is framework-derived from the principal, not a request param).
//
// ref: docs/architecture/202606130635-1939-adr-framework-owned-contract.md
// ref: docs/plans/specs/1895-device-identity-cert-framework/spec.md FR-013

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// validErrorBody is the shared error-response envelope shape; reused for every
// declared 4xx/5xx auth-boundary assertion (requestId is framework-injected and
// not required by the schema).
var validErrorBody = []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`)

func TestDeviceIdentityEnroll_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.enroll.v1")

	// normal schema (csr is base64; no client-passed tenantId)
	c.ValidateRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1"}`))
	c.ValidateRequest(t, []byte(`{
		"csr": "Q1NSREVS", "deviceId": "dev-1", "requestedDuration": "2160h",
		"usages": ["client auth", "digital signature"],
		"subjectAltNames": {"dnsNames": ["dev1.example.com"]}
	}`))
	c.ValidateResponse(t, []byte(`{
		"data": {
			"certificate": "Y2VydA==", "chain": "Y2hhaW4=", "certRef": {"issuer": "softca", "serial": "01ab"},
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
			"epoch": 0, "status": "issued", "deviceId": "dev-1"
		}
	}`))

	// parameter errors
	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1"}`))                      // missing csr
	c.MustRejectRequest(t, []byte(`{"csr":"Q1NSREVS"}`))                        // missing deviceId
	c.MustRejectRequest(t, []byte(`{"csr":"","deviceId":"dev-1"}`))             // empty csr (minLength:1)
	c.MustRejectRequest(t, []byte(`{"csr":"not base64!!","deviceId":"dev-1"}`)) // F4: non-base64 csr rejected
	c.MustRejectRequest(t, []byte(`{
		"csr": "Q1NSREVS", "deviceId": "dev-1", "requestedDuration": "forever"
	}`)) // invalid requestedDuration pattern
	// NOTE: keyUsage allow-set is NOT enforced at the wire layer — contractgen cannot
	// generate typed enums for array items, and the allowed-usage set is the Signer's
	// SignConstraints responsibility (spec FR-005, PR-5). usages is a free-form string array here.
	c.MustRejectResponse(t, []byte(`{
		"data": {
			"certificate": "Y2VydA==", "chain": "Y2hhaW4=", "certRef": {"issuer": "softca", "serial": "01ab"},
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
			"epoch": 0, "status": "bogus"
		}
	}`)) // bad certStatus enum
	c.MustRejectResponse(t, []byte(`{"data":{"certRef":{"issuer":"softca","serial":"01ab"},"status":"issued"}}`)) // missing cert material

	// auth-boundary + service-availability declaration (every declared status conforms)
	c.ValidateErrorResponse(t, 400, validErrorBody)
	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 403, validErrorBody)
	c.ValidateErrorResponse(t, 409, validErrorBody)
	c.ValidateErrorResponse(t, 413, validErrorBody)
	c.ValidateErrorResponse(t, 422, validErrorBody)
	c.ValidateErrorResponse(t, 503, validErrorBody)
}

func TestDeviceIdentityRenew_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.renew.v1")

	c.ValidateRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1","priorSerial":"01ab"}`))
	c.ValidateResponse(t, []byte(`{
		"data": {
			"certificate": "Y2VydA==", "chain": "Y2hhaW4=", "certRef": {"issuer": "softca", "serial": "02cd"},
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
			"epoch": 1, "status": "rotated", "deviceId": "dev-1", "priorSerial": "01ab"
		}
	}`))

	c.MustRejectRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1"}`))                     // missing priorSerial
	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1","priorSerial":"01ab"}`))                 // missing csr
	c.MustRejectRequest(t, []byte(`{"csr":"","deviceId":"dev-1","priorSerial":"01ab"}`))        // empty csr (minLength:1)
	c.MustRejectRequest(t, []byte(`{"csr":"Q1NSREVS","deviceId":"dev-1","priorSerial":"xyz"}`)) // non-hex priorSerial

	c.MustRejectResponse(t, []byte(`{
		"data": {
			"certificate": "Y2VydA==", "chain": "Y2hhaW4=", "certRef": {"issuer": "softca", "serial": "02cd"},
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
			"epoch": 1, "status": "bogus"
		}
	}`)) // bad certStatus enum
	c.MustRejectResponse(t, []byte(`{"data":{"certRef":{"issuer":"softca","serial":"02cd"},"status":"rotated"}}`)) // missing cert material

	c.ValidateErrorResponse(t, 400, validErrorBody)
	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 403, validErrorBody)
	c.ValidateErrorResponse(t, 404, validErrorBody)
	c.ValidateErrorResponse(t, 413, validErrorBody)
	c.ValidateErrorResponse(t, 422, validErrorBody)
	c.ValidateErrorResponse(t, 503, validErrorBody)
}

func TestDeviceIdentityRevoke_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.revoke.v1")

	c.ValidateRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"01ab"},"deviceId":"dev-1","reason":"keyCompromise"}`))
	c.ValidateRequest(t, []byte(`{
		"certRef": {"issuer": "softca", "serial": "01ab"}, "deviceId": "dev-1", "reason": "superseded"
	}`))
	c.ValidateResponse(t, []byte(`{
		"data": {"certRef": {"issuer": "softca", "serial": "01ab"}, "status": "revoked", "reason": "keyCompromise",
		"revokedAt": "2026-06-13T00:00:00Z"}
	}`))

	// missing certRef
	c.MustRejectRequest(t, []byte(`{"deviceId":"dev-1","reason":"keyCompromise"}`))
	// missing reason
	c.MustRejectRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"01ab"},"deviceId":"dev-1"}`))
	// bad reason enum
	c.MustRejectRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"01ab"},"deviceId":"dev-1","reason":"bogus"}`))
	// empty deviceId (minLength:1)
	c.MustRejectRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"01ab"},"deviceId":"","reason":"keyCompromise"}`))
	// non-hex certRef.serial (shared hex pattern)
	c.MustRejectRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"xyz"},"deviceId":"dev-1","reason":"keyCompromise"}`))
	// F3: removeFromCRL dropped from the revoke reason enum
	c.MustRejectRequest(t, []byte(`{"certRef":{"issuer":"softca","serial":"01ab"},"deviceId":"dev-1","reason":"removeFromCRL"}`))

	c.MustRejectResponse(t, []byte(`{
		"data": {"certRef": {"issuer": "softca", "serial": "01ab"}, "status": "bogus", "reason": "keyCompromise",
		"revokedAt": "2026-06-13T00:00:00Z"}
	}`)) // bad status enum
	c.MustRejectResponse(t, []byte(`{
		"data": {"certRef": {"issuer": "softca", "serial": "01ab"}, "status": "revoked", "reason": "bogus",
		"revokedAt": "2026-06-13T00:00:00Z"}
	}`)) // bad reason enum

	c.ValidateErrorResponse(t, 400, validErrorBody)
	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 403, validErrorBody) // cross-scope fail-closed
	c.ValidateErrorResponse(t, 404, validErrorBody)
	c.ValidateErrorResponse(t, 413, validErrorBody)
	c.ValidateErrorResponse(t, 422, validErrorBody)
	c.ValidateErrorResponse(t, 503, validErrorBody)
}

func TestDeviceIdentityStatus_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.deviceidentity.status.v1")

	// normal response (certRef issuer+serial)
	c.ValidateResponse(t, []byte(`{
		"data": {
			"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "status": "active",
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z", "epoch": 0
		}
	}`))
	c.MustRejectResponse(t, []byte(`{
		"data": {
			"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "status": "bogus",
			"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z", "epoch": 0
		}
	}`))

	// query param validation (FMT-25 maxLength). Status is keyed solely by deviceId:
	// serial/issuer are not query params (a bare serial is never a lookup key — epic #1895
	// FR-007), so a half-specified cert lookup is structurally unrepresentable, not asserted.
	c.ValidateQueryParam(t, "deviceId", "dev-1")
	c.MustRejectQueryParam(t, "deviceId", strings.Repeat("x", 257))

	c.ValidateErrorResponse(t, 400, validErrorBody)
	c.ValidateErrorResponse(t, 401, validErrorBody)
	c.ValidateErrorResponse(t, 403, validErrorBody)
	c.ValidateErrorResponse(t, 404, validErrorBody)
}

func TestDeviceIdentityCertIssuedEvent_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "event.deviceidentity.cert-issued.v1")

	c.ValidatePayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "epoch": 0,
		"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
		"action": "enrolled", "actorId": "act-1"
	}`))
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-abc"}`))

	c.MustRejectHeaders(t, []byte(`{}`)) // missing required eventId

	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "epoch": 0, "notBefore": "2026-06-13T00:00:00Z",
		"notAfter": "2027-06-13T00:00:00Z", "action": "enrolled", "actorId": "act-1"
	}`)) // missing certRef
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "xyz"}, "epoch": 0,
		"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
		"action": "enrolled", "actorId": "act-1"
	}`)) // F4: non-hex certRef.serial rejected by the shared pattern
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "epoch": 0,
		"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
		"action": "bogus", "actorId": "act-1"
	}`)) // bad action enum
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "epoch": 0,
		"notBefore": "2026-06-13T00:00:00Z", "notAfter": "2027-06-13T00:00:00Z",
		"action": "enrolled", "actorId": "act-1", "certificate": "leak"
	}`)) // unevaluated field — no cert/key leak (unevaluatedProperties:false)
}

func TestDeviceIdentityCertRevokedEvent_V1(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "event.deviceidentity.cert-revoked.v1")

	c.ValidatePayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "reason": "keyCompromise",
		"revokedAt": "2026-06-13T00:00:00Z", "actorId": "act-1"
	}`))
	c.ValidateHeaders(t, []byte(`{"eventId":"evt-def"}`))
	c.MustRejectHeaders(t, []byte(`{}`)) // missing required eventId

	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "reason": "keyCompromise",
		"revokedAt": "2026-06-13T00:00:00Z", "actorId": "act-1"
	}`)) // missing certRef
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "reason": "bogus",
		"revokedAt": "2026-06-13T00:00:00Z", "actorId": "act-1"
	}`)) // bad reason enum
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "reason": "removeFromCRL",
		"revokedAt": "2026-06-13T00:00:00Z", "actorId": "act-1"
	}`)) // F3: removeFromCRL dropped from the event reason enum
	c.MustRejectPayload(t, []byte(`{
		"deviceId": "dev-1", "certRef": {"issuer": "softca", "serial": "01ab"}, "reason": "keyCompromise",
		"revokedAt": "2026-06-13T00:00:00Z", "actorId": "act-1", "privateKey": "leak"
	}`)) // unevaluatedProperties:false guards key leak
}
