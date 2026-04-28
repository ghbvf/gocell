// Package eventtype_only_const_audit_failopen exercises the EventType-only
// detection path: Topic is absent, EventType is set via a package-level const
// variable (not a string literal). Rule must fire because the resolved value
// matches the "audit." security-sensitive prefix.
package eventtype_only_const_audit_failopen

import "fixturetest/outbox"

// auditEventType is an untyped string constant — simulates a domain event-type
// constant passed as EventType without an explicit Topic field.
const auditEventType = "audit.policy-changed.v1"

var _ = outbox.Entry{
	ID:            "x",
	EventType:     auditEventType,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
