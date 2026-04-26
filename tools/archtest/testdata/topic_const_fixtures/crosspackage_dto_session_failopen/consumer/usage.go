// Package consumer exercises cross-package const resolution: Topic is set
// via dto.TopicSessionCreated (a SelectorExpr). The resolver must follow the
// import chain through go/types TypesInfo to retrieve "session.created.v1".
package consumer

import (
	"fixturetest/crosspackage_dto_session_failopen/dto"
	"fixturetest/outbox"
)

var _ = outbox.Entry{
	ID:            "x",
	Topic:         dto.TopicSessionCreated,
	EventType:     dto.TopicSessionCreated,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
