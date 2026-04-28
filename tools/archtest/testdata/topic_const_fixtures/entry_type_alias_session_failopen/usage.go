package entrytypealias

import "fixturetest/outbox"

type EntryAlias = outbox.Entry

var _ = EntryAlias{
	ID:            "evt-entry-alias",
	Topic:         "session.created.v1",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
