// Package samepackage_const_session_failclosed is a negative fixture:
// Topic resolves to a security-sensitive string but FailurePolicy is
// FailClosed (not FailOpen). Rule must NOT fire.
package samepackage_const_session_failclosed

import "fixturetest/outbox"

const sessionTopic = "session.created.v1"

var _ = outbox.Entry{
	ID:            "x",
	Topic:         sessionTopic,
	FailurePolicy: outbox.FailurePolicyFailClosed,
}
