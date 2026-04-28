package topic_empty_event_session_failopen

import "fixturetest/outbox"

var Entry = outbox.Entry{
	Topic:         "",
	EventType:     "session.created.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
