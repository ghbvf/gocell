// invariants asserted in this file:
//   - INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01
//
// CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 — credential-response
// idempotency-exempt codegen guard.
//
// The HTTP idempotency store records+replays responses keyed on the
// Idempotency-Key request header (24h TTL in Redis). Any HTTP contract whose
// response schema carries a sensitive key (per pkg/redaction.IsSensitiveKey)
// at any depth MUST declare endpoints.http.idempotency.exempt: true; without
// it, a credential-bearing response (e.g. accessToken / refreshToken under
// "data") would be replay-eligible and the idempotency store would persist tokens
// into Redis — a credential-leakage vector (issue #1469).
//
// This file holds the BEHAVIORAL coverage: it exercises rejectUnexemptCredential-
// Response directly (unit) and through the buildHTTPDTOs integration path, covering
// positive (exempt), negative (non-vacuity proof), nested credential, and
// non-credential cases.
//
// The call-site allowlist (only buildHTTPDTOs may call the guard on the Response
// wire-out path) lives in the archtest
// tools/archtest/credential_response_idempotency_funnel_test.go.
package contractgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// -------------------------------------------------------------------
// Unit tests for rejectUnexemptCredentialResponse directly.
// -------------------------------------------------------------------

// TestCredentialResponseIdempotencyGuard_NegativeNonVacuity is the NEGATIVE,
// non-vacuity proof: a contract whose response body contains accessToken is
// rejected when idempotencyExempt is false. This test MUST be written (and
// observed to FAIL) before the implementation is added to prove the guard is
// non-vacuous — it cannot pass silently on a missing implementation.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (non-vacuity proof).
func TestCredentialResponseIdempotencyGuard_NegativeNonVacuity(t *testing.T) {
	cases := []struct {
		name    string
		key     string // sensitive key in response schema
		wantSub string // substring the error must mention
	}{
		{
			name:    "accessToken_rejected_when_not_exempt",
			key:     "accessToken",
			wantSub: "accessToken",
		},
		{
			name:    "refreshToken_rejected_when_not_exempt",
			key:     "refreshToken",
			wantSub: "refreshToken",
		},
		{
			name:    "token_rejected_when_not_exempt",
			key:     "token",
			wantSub: "token",
		},
		{
			name:    "password_rejected_when_not_exempt",
			key:     "password",
			wantSub: "password",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &Schema{
				Type:          "object",
				Properties:    map[string]*Schema{tc.key: {Type: "string"}},
				PropertyOrder: []string{tc.key},
			}
			err := rejectUnexemptCredentialResponse("http.auth.synth.v1", false, resp)
			if err == nil {
				t.Fatalf("expected guard to reject response with %q field when idempotency.exempt=false, got nil", tc.key)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must mention offending field %q", err.Error(), tc.wantSub)
			}
			if !strings.Contains(err.Error(), "idempotency.exempt") {
				t.Errorf("error %q must mention the fix (idempotency.exempt)", err.Error())
			}
		})
	}
}

// TestCredentialResponseIdempotencyGuard_ExemptPasses asserts that
// idempotencyExempt:true suppresses the guard regardless of the response fields.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (exempt positive).
func TestCredentialResponseIdempotencyGuard_ExemptPasses(t *testing.T) {
	resp := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"accessToken":  {Type: "string"},
			"refreshToken": {Type: "string"},
		},
		PropertyOrder: []string{"accessToken", "refreshToken"},
	}
	if err := rejectUnexemptCredentialResponse("http.auth.login.v1", true, resp); err != nil {
		t.Fatalf("idempotencyExempt=true must suppress the guard, got: %v", err)
	}
}

// TestCredentialResponseIdempotencyGuard_NilSchemaOK asserts that a nil response
// schema (no response body, e.g. 204 noContent) never triggers the guard.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (nil schema edge).
func TestCredentialResponseIdempotencyGuard_NilSchemaOK(t *testing.T) {
	if err := rejectUnexemptCredentialResponse("http.auth.synth.v1", false, nil); err != nil {
		t.Fatalf("nil schema must not trigger the guard, got: %v", err)
	}
}

// TestCredentialResponseIdempotencyGuard_NonCredentialPasses asserts that a
// response without any sensitive key passes the guard even when not exempt.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (non-credential).
func TestCredentialResponseIdempotencyGuard_NonCredentialPasses(t *testing.T) {
	resp := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"userId":        {Type: "string"},
			"email":         {Type: "string"},
			"correlationId": {Type: "string"},
		},
		PropertyOrder: []string{"userId", "email", "correlationId"},
	}
	if err := rejectUnexemptCredentialResponse("http.user.profile.v1", false, resp); err != nil {
		t.Fatalf("non-credential response must pass, got: %v", err)
	}
}

// TestCredentialResponseIdempotencyGuard_NestedCredentialCaught asserts that a
// credential nested under data.tokens.accessToken is caught at any depth.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (nested credential).
func TestCredentialResponseIdempotencyGuard_NestedCredentialCaught(t *testing.T) {
	resp := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"data": {
				Type: "object",
				Properties: map[string]*Schema{
					"tokens": {
						Type: "object",
						Properties: map[string]*Schema{
							"accessToken": {Type: "string"},
						},
						PropertyOrder: []string{"accessToken"},
					},
				},
				PropertyOrder: []string{"tokens"},
			},
		},
		PropertyOrder: []string{"data"},
	}
	err := rejectUnexemptCredentialResponse("http.auth.synth.v1", false, resp)
	if err == nil {
		t.Fatal("nested credential under data.tokens.accessToken must be caught")
	}
	if !strings.Contains(err.Error(), "accessToken") {
		t.Errorf("error %q must mention the offending field", err.Error())
	}
}

// TestCredentialResponseIdempotencyGuard_NestedInArrayCaught asserts that a
// credential nested inside an array's items (e.g. data[].accessToken) is caught.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (array items).
func TestCredentialResponseIdempotencyGuard_NestedInArrayCaught(t *testing.T) {
	resp := &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"data": {
				Type: "array",
				Items: &Schema{
					Type: "object",
					Properties: map[string]*Schema{
						"accessToken": {Type: "string"},
					},
					PropertyOrder: []string{"accessToken"},
				},
			},
		},
		PropertyOrder: []string{"data"},
	}
	err := rejectUnexemptCredentialResponse("http.auth.synth.v1", false, resp)
	if err == nil {
		t.Fatal("credential in array items must be caught")
	}
	if !strings.Contains(err.Error(), "accessToken") {
		t.Errorf("error %q must mention the offending field", err.Error())
	}
}

// -------------------------------------------------------------------
// Integration tests through buildHTTPDTOs.
// -------------------------------------------------------------------

// TestCredentialResponseIdempotencyGuard_Integration_NonExemptRejectsCredentialResponse
// is the end-to-end proof that buildHTTPDTOs rejects a contract with a credential
// response schema when idempotencyExempt is not set.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (integration negative).
func TestCredentialResponseIdempotencyGuard_Integration_NonExemptRejectsCredentialResponse(t *testing.T) {
	tmp := t.TempDir()
	respBody := flatObject(`"accessToken":{"type":"string"},"refreshToken":{"type":"string"}`)
	writeSensitiveGuardSchema(t, tmp, "response.schema.json", respBody)

	contract := &metadata.ContractMeta{
		ID:         "http.auth.synth.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Idempotency: metadata.HTTPIdempotencyMeta{Exempt: false},
			},
		},
	}
	_, err := buildHTTPDTOs(tmp, contract, synthContractDir, nil, nil, nil)
	if err == nil {
		t.Fatal("expected buildHTTPDTOs to reject credential response without idempotency.exempt, got nil error")
	}
	if !strings.Contains(err.Error(), "idempotency.exempt") {
		t.Errorf("error %q must mention idempotency.exempt fix", err.Error())
	}
}

// TestCredentialResponseIdempotencyGuard_Integration_ExemptPasses verifies that
// setting idempotency.exempt: true suppresses the guard in buildHTTPDTOs.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (integration positive).
func TestCredentialResponseIdempotencyGuard_Integration_ExemptPasses(t *testing.T) {
	tmp := t.TempDir()
	respBody := flatObject(`"accessToken":{"type":"string"},"refreshToken":{"type":"string"}`)
	writeSensitiveGuardSchema(t, tmp, "response.schema.json", respBody)

	contract := &metadata.ContractMeta{
		ID:         "http.auth.login.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Idempotency: metadata.HTTPIdempotencyMeta{Exempt: true},
			},
		},
	}
	_, err := buildHTTPDTOs(tmp, contract, synthContractDir, nil, nil, nil)
	if err != nil {
		t.Fatalf("idempotency.exempt=true must suppress the credential guard in buildHTTPDTOs, got: %v", err)
	}
}

// TestCredentialResponseIdempotencyGuard_Integration_NestedCredentialRejected
// proves the guard catches tokens nested under "data" (the pattern used by the
// 3 flagged contracts: http.auth.login.v1 / refresh.v1 / change-password.v1).
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (integration nested).
func TestCredentialResponseIdempotencyGuard_Integration_NestedCredentialRejected(t *testing.T) {
	tmp := t.TempDir()
	// Mirrors the actual login response structure: data.accessToken / data.refreshToken.
	respBody := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "data": {
      "type": "object",
      "properties": {
        "accessToken":  { "type": "string" },
        "refreshToken": { "type": "string" }
      }
    }
  }
}`
	writeSensitiveGuardSchema(t, tmp, "response.schema.json", respBody)

	contract := &metadata.ContractMeta{
		ID:         "http.auth.synth.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Idempotency: metadata.HTTPIdempotencyMeta{Exempt: false},
			},
		},
	}
	_, err := buildHTTPDTOs(tmp, contract, synthContractDir, nil, nil, nil)
	if err == nil {
		t.Fatal("nested credential response without idempotency.exempt must be rejected")
	}
	if !strings.Contains(err.Error(), "accessToken") && !strings.Contains(err.Error(), "refreshToken") {
		t.Errorf("error %q must name the offending field", err.Error())
	}
}
