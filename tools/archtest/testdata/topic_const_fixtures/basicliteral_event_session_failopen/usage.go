// Package basicliteral_event_session_failopen is a fixture that uses the
// project contract topic shape directly in the Topic field. Expects
// OUTBOX-TOPIC-FAILOPEN-01 to fire.
package basicliteral_event_session_failopen

import "fixturetest/outbox"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         "event.session.created.v1",
	EventType:     "event.session.created.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
