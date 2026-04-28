package importaliassession

import ob "fixturetest/outbox"

var _ = ob.Entry{
	ID:            "evt-import-alias",
	Topic:         "session.created.v1",
	FailurePolicy: ob.FailurePolicyFailOpen,
}
