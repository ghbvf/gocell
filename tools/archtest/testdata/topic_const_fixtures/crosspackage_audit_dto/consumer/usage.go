// Package consumer exercises cross-package const resolution for a non-session
// security topic: Topic is set via dto.TopicAuditEntryAppended (SelectorExpr).
// Expects OUTBOX-TOPIC-FAILOPEN-01 to fire because "audit." is security-sensitive.
package consumer

import (
	"fixturetest/crosspackage_audit_dto/dto"
	"fixturetest/outbox"
)

var _ = outbox.Entry{
	ID:            "x",
	Topic:         dto.TopicAuditEntryAppended,
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
