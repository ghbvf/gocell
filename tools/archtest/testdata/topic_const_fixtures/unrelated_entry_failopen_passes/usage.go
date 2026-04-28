package unrelatedentry

import "fixturetest/outbox"

type Entry struct {
	ID            string
	Topic         string
	FailurePolicy outbox.FailurePolicy
}

var _ = Entry{
	ID:            "evt-unrelated-entry",
	Topic:         "session.created.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
