package dynamic_failopen_policy_session

import "fixturetest/outbox"

var policy = outbox.FailurePolicyFailOpen

var Entry = outbox.Entry{
	Topic:         "session.created.v1",
	FailurePolicy: policy,
}
