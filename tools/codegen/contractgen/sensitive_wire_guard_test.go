// invariants asserted in this file:
//   - INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01
//
// AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 — audit-domain codegen funnel.
//
// The audit cell (auditcore) is the one consumer that aggregates the Principal
// metadata of *whoever* triggered each audited event. Re-projecting a third
// party's sessionId (or any other pkg/redaction sensitive-key field) onto an
// audit wire-out schema leaks a credential-adjacent token to anyone who can read
// the audit log (issue #1219). Unlike the auth domain — where sessionId is the
// caller's *own* resource id and a legitimate wire field (login response,
// session.created payload, etc.) — sensitivity in the audit domain is
// unconditional.
//
// So contractgen REJECTS, at generation time, any wire-out schema (HTTP response
// or event payload) under an audit-domain contract that declares a property
// whose name matches pkg/redaction.IsSensitiveKey, at any nesting depth. The
// offending DTO can never be produced → leaking the field is unexpressible.
//
// AI-robust grading (Hard 范本目录: codegen funnel + golden — single source of
// truth is the funnel godoc in sensitive_wire_guard.go + the archtest
// audit_wire_sensitive_funnel_test.go; this block must not drift from them):
//   - Upstream Hard: a sensitive field cannot reach a generated audit DTO. By
//     schema → generation rejects it here; by hand-edited types_gen.go →
//     `gocell verify generated` byte-stable golden drift fails CI.
//   - Downstream Medium (ceiling): the funnel must be CALLED on every audit
//     wire-out path (Response + Payload + Headers). That "must-call" cannot be
//     expressed in Go's type system (schemaToDTOs is shared with the exempt
//     Request path), so it is locked by archtest
//     AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (call-site allowlist + role coverage).
//     archtest-Medium is the Go ceiling for codegen must-call — a permanent
//     won't-do ceiling (same shape as #851/#893/#982/#1282). An optional
//     schemaToWireOutDTOs wrapper could strengthen the archtest-Medium coverage
//     but does not change the tier; it is left as optional defense-in-depth,
//     not separately tracked.
//   - Scope: audit domain only (规则不超前于代码现状 — auditcore is today's only
//     Principal-aggregating consumer). When a new such cell appears, extend
//     isAuditWireContract in the same PR.
//
// This file holds the BEHAVIORAL coverage: it drives the funnel through the
// real generation entry points (buildHTTPDTOs / buildEventSpec) so the
// rejection/exemption behavior is pinned. The call-site allowlist (funnel is
// wired into exactly the Response + Payload + Headers wire-out paths, never
// Request) and the domain scope-completeness lock live in the archtest
// tools/archtest/audit_wire_sensitive_funnel_test.go — those are the
// AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 enforcement points discovered by
// ARCHTEST-VERIFY-COVERAGE-01; this file is an ordinary contractgen unit test.
package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// synthContractDir is the fixed contract-relative dir every sensitive-guard
// test writes its synthetic schemas under; it doubles as the contractDir passed
// to buildHTTPDTOs / buildEventSpec.
const synthContractDir = "contracts/synth/v1"

// writeSensitiveGuardSchema writes a JSON Schema file under rootDir/synthContractDir
// and returns nothing; t.Fatalf on failure.
func writeSensitiveGuardSchema(t *testing.T, rootDir, name, body string) {
	t.Helper()
	dir := filepath.Join(rootDir, synthContractDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// listShapedResponse mirrors http.audit.list.v1: data[] of objects, where the
// sensitive field is nested two levels deep (object → array → items → object).
// Proves the funnel recurses through Properties AND Items.
func listShapedResponse(itemFields string) string {
	return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "data": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {` + itemFields + `}
      }
    },
    "nextCursor": { "type": "string" },
    "hasMore": { "type": "boolean" }
  }
}`
}

func flatObject(fields string) string {
	return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {` + fields + `}
}`
}

// TestAuditWireSensitiveFieldFunnel_Response asserts the funnel fires on audit
// HTTP response schemas, recurses into nested array items, is gated to the audit
// domain, and never touches the request path.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (behavioral funnel test).
func TestAuditWireSensitiveFieldFunnel_Response(t *testing.T) {
	cases := []struct {
		name              string
		contractID        string
		respBody          string
		idempotencyExempt bool // set true for non-audit contracts whose response carries a sensitive key
		wantErr           bool
		wantSub           string // substring the error must mention
	}{
		{
			name:       "audit_response_nested_sessionId_rejected",
			contractID: "http.audit.list.v1",
			respBody:   listShapedResponse(`"id":{"type":"string"},"sessionId":{"type":"string"}`),
			wantErr:    true,
			wantSub:    "sessionId",
		},
		{
			name:       "audit_response_nested_secret_rejected",
			contractID: "http.audit.synth.v1",
			respBody:   listShapedResponse(`"id":{"type":"string"},"secret":{"type":"string"}`),
			wantErr:    true,
			wantSub:    "secret",
		},
		{
			name:       "audit_response_correlationId_ok",
			contractID: "http.audit.list.v1",
			respBody:   listShapedResponse(`"id":{"type":"string"},"correlationId":{"type":"string"},"subjectId":{"type":"string"}`),
			wantErr:    false,
		},
		{
			// subjectId is the OAuth subject-of-record (stable identity), NOT in
			// pkg/redaction's sensitive-key set — explicitly safe to project.
			name:       "audit_response_subjectId_safe",
			contractID: "http.audit.list.v1",
			respBody:   listShapedResponse(`"subjectId":{"type":"string"}`),
			wantErr:    false,
		},
		{
			// Non-audit auth contracts may legitimately return sessionId (the
			// caller's own session resource id). idempotencyExempt: true is set
			// here as required by CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01
			// — the real http.auth.login.v1 contract already carries this flag.
			name:              "non_audit_response_sessionId_ok",
			contractID:        "http.auth.login.v1",
			respBody:          flatObject(`"sessionId":{"type":"string"}`),
			idempotencyExempt: true,
			wantErr:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeSensitiveGuardSchema(t, tmp, "response.schema.json", tc.respBody)

			contract := &metadata.ContractMeta{
				ID:         tc.contractID,
				Kind:       "http",
				SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Idempotency: metadata.HTTPIdempotencyMeta{Exempt: tc.idempotencyExempt},
					},
				},
			}
			_, err := buildHTTPDTOs(tmp, contract, synthContractDir, nil, nil)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected funnel to reject %s, got nil error", tc.contractID)
				}
				if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q must mention offending field %q", err.Error(), tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.contractID, err)
			}
		})
	}
}

// TestAuditWireSensitiveFieldFunnel_FallbackUnordered exercises the fail-closed
// fallback in walkAuditWireSensitive: a Schema whose Properties map carries a
// sensitive key NOT enumerated in PropertyOrder must still be rejected. Parse
// always populates PropertyOrder, so a hand-built Schema is the only way to
// cover the fallback map walk.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (fallback branch).
func TestAuditWireSensitiveFieldFunnel_FallbackUnordered(t *testing.T) {
	s := &Schema{
		Type:       "object",
		Properties: map[string]*Schema{"sessionId": {Type: "string"}},
		// PropertyOrder deliberately empty — forces the fallback Properties walk.
	}
	if err := rejectSensitiveAuditWireFields("http.audit.list.v1", "response", s); err == nil {
		t.Fatal("fallback walk must reject sessionId even when PropertyOrder is empty")
	}
	// Non-audit domain stays exempt regardless of the fallback path.
	if err := rejectSensitiveAuditWireFields("http.auth.login.v1", "response", s); err != nil {
		t.Fatalf("non-audit contract must be exempt, got: %v", err)
	}
}

// TestAuditWireSensitiveFieldFunnel_RequestExempt asserts the request path is
// never funneled — an audit-domain request schema may legitimately carry a
// password-like field (the funnel covers wire-OUT only).
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (behavioral funnel test).
func TestAuditWireSensitiveFieldFunnel_RequestExempt(t *testing.T) {
	tmp := t.TempDir()
	writeSensitiveGuardSchema(t, tmp, "request.schema.json",
		flatObject(`"password":{"type":"string"}`))

	contract := &metadata.ContractMeta{
		ID:         "http.audit.synth.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Request: "request.schema.json"},
	}
	if _, err := buildHTTPDTOs(tmp, contract, synthContractDir, nil, nil); err != nil {
		t.Fatalf("request path must be exempt from the audit funnel, got: %v", err)
	}
}

// TestAuditWireSensitiveFieldFunnel_Payload asserts the funnel fires on audit
// event payload schemas and is gated to the audit domain.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (behavioral funnel test).
func TestAuditWireSensitiveFieldFunnel_Payload(t *testing.T) {
	cases := []struct {
		name        string
		contractID  string
		payloadBody string
		wantErr     bool
	}{
		{
			name:        "audit_payload_sessionId_rejected",
			contractID:  "event.audit.appended.v1",
			payloadBody: flatObject(`"eventId":{"type":"string"},"sessionId":{"type":"string"}`),
			wantErr:     true,
		},
		{
			name:        "audit_payload_correlationId_ok",
			contractID:  "event.audit.appended.v1",
			payloadBody: flatObject(`"eventId":{"type":"string"},"correlationId":{"type":"string"}`),
			wantErr:     false,
		},
		{
			name:        "non_audit_payload_sessionId_ok",
			contractID:  "event.session.created.v1",
			payloadBody: flatObject(`"sessionId":{"type":"string"}`),
			wantErr:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeSensitiveGuardSchema(t, tmp, "payload.schema.json", tc.payloadBody)

			contract := &metadata.ContractMeta{
				ID:         tc.contractID,
				Kind:       "event",
				SchemaRefs: metadata.SchemaRefsMeta{Payload: "payload.schema.json"},
			}
			err := buildEventSpec(&ContractGenSpec{}, tmp, contract, synthContractDir)

			if tc.wantErr && err == nil {
				t.Fatalf("expected funnel to reject %s payload, got nil error", tc.contractID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s payload: %v", tc.contractID, err)
			}
		})
	}
}

// TestAuditWireSensitiveFieldFunnel_Headers asserts the funnel also fires on
// audit event *headers* schemas — event.audit.appended.v1 ships a real headers
// schema, so a sensitive key added there must be rejected the same as in the
// payload. The payload below is always clean; only the headers vary.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (behavioral funnel test).
func TestAuditWireSensitiveFieldFunnel_Headers(t *testing.T) {
	cases := []struct {
		name        string
		contractID  string
		headersBody string
		wantErr     bool
	}{
		{
			name:        "audit_headers_sessionId_rejected",
			contractID:  "event.audit.appended.v1",
			headersBody: flatObject(`"eventId":{"type":"string"},"sessionId":{"type":"string"}`),
			wantErr:     true,
		},
		{
			name:        "audit_headers_correlationId_ok",
			contractID:  "event.audit.appended.v1",
			headersBody: flatObject(`"eventId":{"type":"string"},"correlationId":{"type":"string"}`),
			wantErr:     false,
		},
		{
			name:        "non_audit_headers_sessionId_ok",
			contractID:  "event.session.created.v1",
			headersBody: flatObject(`"eventId":{"type":"string"},"sessionId":{"type":"string"}`),
			wantErr:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeSensitiveGuardSchema(t, tmp, "payload.schema.json",
				flatObject(`"eventId":{"type":"string"}`))
			writeSensitiveGuardSchema(t, tmp, "headers.schema.json", tc.headersBody)

			contract := &metadata.ContractMeta{
				ID:   tc.contractID,
				Kind: "event",
				SchemaRefs: metadata.SchemaRefsMeta{
					Payload: "payload.schema.json",
					Headers: "headers.schema.json",
				},
			}
			err := buildEventSpec(&ContractGenSpec{}, tmp, contract, synthContractDir)

			if tc.wantErr && err == nil {
				t.Fatalf("expected funnel to reject %s headers, got nil error", tc.contractID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s headers: %v", tc.contractID, err)
			}
		})
	}
}
