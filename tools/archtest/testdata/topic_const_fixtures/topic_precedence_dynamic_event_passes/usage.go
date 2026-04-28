package topic_precedence_dynamic_event_passes

import "fixturetest/outbox"

func dynamicEvent() string {
	return "session.created.v1"
}

var Entry = outbox.Entry{
	Topic:         "metrics.safe.v1",
	EventType:     dynamicEvent(),
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
