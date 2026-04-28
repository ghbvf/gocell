package consumer

import (
	"fixturetest/crosspackage_failopen_alias/policy"
	"fixturetest/outbox"
)

var _ = outbox.Entry{
	ID:            "evt-cross-failopen-alias",
	Topic:         "session.created.v1",
	FailurePolicy: policy.SecurityFailOpen,
}
