package localfailopenalias

import "fixturetest/outbox"

const localFailOpen = outbox.FailurePolicyFailOpen

var _ = outbox.Entry{
	ID:            "evt-local-failopen-alias",
	Topic:         "session.created.v1",
	FailurePolicy: localFailOpen,
}
