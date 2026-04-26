package samepackage_const_session_failopen

import "fixturetest/outbox"

// sessionTopic is defined in consts.go. The resolver must evaluate it through
// go/types TypesInfo to reach the underlying string "session.created.v1".
var _ = outbox.Entry{
	ID:            "x",
	Topic:         sessionTopic,
	EventType:     sessionTopic,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
