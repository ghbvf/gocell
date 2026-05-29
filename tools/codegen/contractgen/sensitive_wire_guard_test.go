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
// AI-robust grading (Hard 范本目录: codegen funnel + golden):
//   - Downstream Hard: generation rejects the schema; gated to the audit
//     wire-out paths (Response in buildHTTPDTOs + Payload in buildEventSpec),
//     never the Request path (inbound password is legitimate).
//   - Upstream Hard: `gocell verify generated` byte-stable golden drift catches
//     a hand-edited types_gen.go; `gocell generate` re-derivation re-rejects.
//   - Scope: audit domain only (规则不超前于代码现状 — auditcore is today's only
//     Principal-aggregating consumer). When a new such cell appears, extend
//     isAuditWireContract in the same PR.
//
// This file tests the funnel through the real generation entry points
// (buildHTTPDTOs / buildEventSpec) rather than the private validator, so it
// locks both the rejection behavior AND its wiring into exactly the wire-out
// paths.
package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// writeSensitiveGuardSchema writes a JSON Schema file under rootDir at the given
// contract-relative path and returns nothing; t.Fatalf on failure.
func writeSensitiveGuardSchema(t *testing.T, rootDir, contractDir, name, body string) {
	t.Helper()
	dir := filepath.Join(rootDir, contractDir)
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
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01
func TestAuditWireSensitiveFieldFunnel_Response(t *testing.T) {
	cases := []struct {
		name       string
		contractID string
		respBody   string
		wantErr    bool
		wantSub    string // substring the error must mention
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
			name:       "non_audit_response_sessionId_ok",
			contractID: "http.auth.login.v1",
			respBody:   flatObject(`"sessionId":{"type":"string"}`),
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			contractDir := "contracts/synth/v1"
			writeSensitiveGuardSchema(t, tmp, contractDir, "response.schema.json", tc.respBody)

			contract := &metadata.ContractMeta{
				ID:         tc.contractID,
				Kind:       "http",
				SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
			}
			_, err := buildHTTPDTOs(tmp, contract, contractDir, nil, nil)

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

// TestAuditWireSensitiveFieldFunnel_RequestExempt asserts the request path is
// never funneled — an audit-domain request schema may legitimately carry a
// password-like field (the funnel covers wire-OUT only).
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01
func TestAuditWireSensitiveFieldFunnel_RequestExempt(t *testing.T) {
	tmp := t.TempDir()
	contractDir := "contracts/synth/v1"
	writeSensitiveGuardSchema(t, tmp, contractDir, "request.schema.json",
		flatObject(`"password":{"type":"string"}`))

	contract := &metadata.ContractMeta{
		ID:         "http.audit.synth.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Request: "request.schema.json"},
	}
	if _, err := buildHTTPDTOs(tmp, contract, contractDir, nil, nil); err != nil {
		t.Fatalf("request path must be exempt from the audit funnel, got: %v", err)
	}
}

// TestAuditWireSensitiveFieldFunnel_Payload asserts the funnel fires on audit
// event payload schemas and is gated to the audit domain.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01
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
			contractDir := "contracts/synth/v1"
			writeSensitiveGuardSchema(t, tmp, contractDir, "payload.schema.json", tc.payloadBody)

			contract := &metadata.ContractMeta{
				ID:         tc.contractID,
				Kind:       "event",
				SchemaRefs: metadata.SchemaRefsMeta{Payload: "payload.schema.json"},
			}
			err := buildEventSpec(&ContractGenSpec{}, tmp, contract, contractDir)

			if tc.wantErr && err == nil {
				t.Fatalf("expected funnel to reject %s payload, got nil error", tc.contractID)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s payload: %v", tc.contractID, err)
			}
		})
	}
}
