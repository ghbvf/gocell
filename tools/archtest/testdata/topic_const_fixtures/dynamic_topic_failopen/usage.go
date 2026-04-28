package dynamictopic

import "fixturetest/outbox"

var topic = "session.created.v1"

var _ = outbox.Entry{
	ID:            "evt-dynamic-topic",
	Topic:         topic,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
