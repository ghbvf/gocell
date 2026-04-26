// Package basicliteral_session_failopen is a fixture that uses a BasicLit
// string directly in the Topic field. Expects OUTBOX-TOPIC-FAILOPEN-01 to fire.
package basicliteral_session_failopen

import "fixturetest/outbox"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         "session.created.v1",
	EventType:     "session.created.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
