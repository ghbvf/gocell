package contractgen

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 — audit-domain wire-out sensitive-field
// rejection. Invariant statement + AI-robust grading live in
// tools/archtest/audit_wire_sensitive_funnel_test.go (the archtest that locks
// this funnel's call sites). The behavioral pos/neg coverage lives in
// tools/codegen/contractgen/sensitive_wire_guard_test.go.
//
// The audit cell aggregates the Principal metadata of whoever triggered each
// audited event, so projecting a third party's sessionId (or any other
// pkg/redaction sensitive-key field) onto an audit wire-out schema leaks a
// credential-adjacent token. Unlike the auth domain — where sessionId is the
// caller's own resource id and a legitimate wire field (login response,
// session.created payload) — sensitivity here is unconditional, so the funnel
// rejects such schemas at generation time and the leaking DTO is never produced.
//
// AI-robust grading (Hard 范本目录: codegen funnel + golden):
//   - Upstream Hard: a sensitive field cannot reach a generated audit DTO. By
//     schema → generation rejects it here; by hand-edited types_gen.go →
//     `gocell verify generated` byte-stable golden drift fails CI.
//   - Downstream Medium (ceiling): the funnel must be CALLED on every audit
//     wire-out path. That "must-call" cannot be expressed in Go's type system
//     (schemaToDTOs is shared with the exempt Request path), so it is locked by
//     archtest AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (call-site allowlist +
//     coverage). archtest-Medium is the Go ceiling for codegen must-call — a
//     permanent won't-do ceiling (same shape as #851/#893/#982/#1282). An
//     optional schemaToWireOutDTOs wrapper could co-locate the funnel with
//     wire-out construction, but it stays archtest-Medium (does not change the
//     tier) and is left as optional defense-in-depth, not separately tracked.
//
// Scope: audit domain only (规则不超前于代码现状 — auditcore is today's sole
// Principal-aggregating consumer). To extend when another such cell appears:
//  1. add the new cell's contractID prefix to isAuditWireContract below;
//  2. add a "<cell>_response_sessionId_rejected" case to
//     sensitive_wire_guard_test.go and confirm the non-audit exempt case still
//     reflects reality;
//  3. the archtest's scope-completeness check (every auditcore-owned contract's
//     id matches a covered prefix) will fail until step 1 lands, so the
//     extension cannot be silently forgotten.
func isAuditWireContract(contractID string) bool {
	return strings.HasPrefix(contractID, "http.audit.") ||
		strings.HasPrefix(contractID, "event.audit.")
}

// rejectSensitiveAuditWireFields returns an error if an audit-domain wire-out
// schema (HTTP response, event payload, or event headers) declares a property
// whose name matches pkg/redaction.IsSensitiveKey, at any nesting depth.
// wireRole labels the schema ("response" / "payload" / "headers") for the error
// message. Non-audit contracts and the inbound request path are exempt —
// callers MUST NOT invoke this for Request schemas (inbound password/token
// fields are legitimate); the archtest enforces that the only call sites are
// the Response, Payload, and Headers wire-out paths.
func rejectSensitiveAuditWireFields(contractID, wireRole string, s *Schema) error {
	if !isAuditWireContract(contractID) {
		return nil
	}
	return walkAuditWireSensitive(contractID, wireRole, "$", s)
}

// walkSchemaSensitiveKeys recurses s (Properties + array Items) and calls
// visit(fieldPath, key) for every property whose name matches
// redaction.IsSensitiveKey, at any nesting depth. path is a JSON-pointer-ish
// breadcrumb ("$", "$.properties.data.items…") passed to visit for diagnostic
// context. The walk is fail-closed: it inspects every entry in
// s.Properties, not only those listed in PropertyOrder, so a future parser
// change that diverges the two cannot silently skip a field.
//
// This is the shared tree-walk used by both the audit-domain sensitive-field
// guard (rejectSensitiveAuditWireFields) and the credential-response idempotency
// guard (rejectUnexemptCredentialResponse). Extracting it eliminates copy-paste
// and ensures both guards evolve together.
func walkSchemaSensitiveKeys(s *Schema, path string, visit func(fieldPath, key string)) {
	if s == nil {
		return
	}
	checked := make(map[string]bool, len(s.Properties))
	walk := func(name string, sub *Schema) {
		if checked[name] {
			return
		}
		checked[name] = true
		fieldPath := path + ".properties." + name
		if redaction.IsSensitiveKey(name) {
			visit(fieldPath, name)
			return // do not recurse into a sensitive node; the visit already reported it
		}
		walkSchemaSensitiveKeys(sub, fieldPath, visit)
	}
	// PropertyOrder first for deterministic ordering.
	for _, name := range s.PropertyOrder {
		walk(name, s.Properties[name])
	}
	// Fail-closed fallback for any property not in PropertyOrder.
	for name, sub := range s.Properties {
		walk(name, sub)
	}
	walkSchemaSensitiveKeys(s.Items, path+".items", visit)
}

// walkAuditWireSensitive recurses the schema tree (Properties + array Items),
// fail-closed: it checks every declared property name, not only those listed in
// PropertyOrder, so a future parser change that diverges the two cannot open a
// gap. path is a JSON-pointer-ish breadcrumb ("$.properties.data.items...")
// included in the error so deep-schema rejections are locatable without a manual
// grep.
func walkAuditWireSensitive(contractID, wireRole, path string, s *Schema) error {
	var firstErr error
	walkSchemaSensitiveKeys(s, path, func(fieldPath, name string) {
		if firstErr != nil {
			return
		}
		firstErr = fmt.Errorf(
			"contract %q %s schema declares sensitive-key field %q at %s: "+
				"audit wire-out schemas must not project Principal credentials "+
				"(AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01)",
			contractID, wireRole, name, fieldPath)
	})
	return firstErr
}
