// Package basicliteral_audit_failopen is a fixture that uses a non-session
// security topic ("audit.entry-appended.v1") with FailurePolicyFailOpen.
// Expects OUTBOX-TOPIC-FAILOPEN-01 to fire because "audit." prefix is in the
// security-sensitive set.
package basicliteral_audit_failopen

import "fixturetest/outbox"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         "audit.entry-appended.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
