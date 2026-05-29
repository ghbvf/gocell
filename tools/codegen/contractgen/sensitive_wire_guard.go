package contractgen

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 — audit-domain wire-out sensitive-field
// rejection. See tools/codegen/contractgen/sensitive_wire_guard_test.go for the
// invariant statement and AI-robust grading.
//
// The audit cell aggregates the Principal metadata of whoever triggered each
// audited event, so projecting a third party's sessionId (or any other
// pkg/redaction sensitive-key field) onto an audit wire-out schema leaks a
// credential-adjacent token. Unlike the auth domain — where sessionId is the
// caller's own resource id — sensitivity here is unconditional, so the funnel
// rejects such schemas at generation time and the leaking DTO is never produced.
//
// Scope is audit-domain only on purpose (规则不超前于代码现状): auditcore is
// today's sole Principal-aggregating consumer. When another such cell appears,
// extend isAuditWireContract in the same PR.
func isAuditWireContract(contractID string) bool {
	return strings.HasPrefix(contractID, "http.audit.") ||
		strings.HasPrefix(contractID, "event.audit.")
}

// rejectSensitiveAuditWireFields returns an error if an audit-domain wire-out
// schema (HTTP response or event payload) declares a property whose name
// matches pkg/redaction.IsSensitiveKey, at any nesting depth. wireRole labels
// the schema ("response" / "payload") for the error message. Non-audit
// contracts and the inbound request path are exempt (callers must not invoke
// this for Request schemas — inbound password/token fields are legitimate).
func rejectSensitiveAuditWireFields(contractID, wireRole string, s *Schema) error {
	if !isAuditWireContract(contractID) {
		return nil
	}
	return walkAuditWireSensitive(contractID, wireRole, s)
}

// walkAuditWireSensitive recurses the schema tree (Properties + array Items),
// fail-closed: it checks every declared property name, not only those listed in
// PropertyOrder, so a future parser change that diverges the two cannot open a
// gap.
func walkAuditWireSensitive(contractID, wireRole string, s *Schema) error {
	if s == nil {
		return nil
	}
	checked := make(map[string]bool, len(s.PropertyOrder))
	check := func(name string, sub *Schema) error {
		if checked[name] {
			return nil
		}
		checked[name] = true
		if redaction.IsSensitiveKey(name) {
			return fmt.Errorf(
				"contract %q %s schema declares sensitive-key field %q: "+
					"audit wire-out schemas must not project Principal credentials "+
					"(AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01)",
				contractID, wireRole, name)
		}
		return walkAuditWireSensitive(contractID, wireRole, sub)
	}
	// PropertyOrder first for deterministic error ordering.
	for _, name := range s.PropertyOrder {
		if err := check(name, s.Properties[name]); err != nil {
			return err
		}
	}
	// Fail-closed fallback for any property not enumerated in PropertyOrder.
	for name, sub := range s.Properties {
		if err := check(name, sub); err != nil {
			return err
		}
	}
	return walkAuditWireSensitive(contractID, wireRole, s.Items)
}
