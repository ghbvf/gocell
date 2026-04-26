// Package consumer exercises cross-package const resolution for project
// contract topic strings such as "event.session.created.v1".
package consumer

import (
	"fixturetest/crosspackage_event_dto_session_failopen/dto"
	"fixturetest/outbox"
)

var _ = outbox.Entry{
	ID:            "x",
	Topic:         dto.TopicSessionCreated,
	EventType:     dto.TopicSessionCreated,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
