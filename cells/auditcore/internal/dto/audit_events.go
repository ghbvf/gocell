// Package dto holds typed payload structs for auditcore event contracts.
//
// JSON field names are camelCase per cell-patterns.md (HTTP DTO 和事件 payload
// 统一 camelCase). The previous snake_case fields on event.audit.appended.v1
// (audit_entry_id / event_type) were retired as part of PR-A6's full-break
// sweep.
package dto

// Topic constants for auditcore events (L2 OutboxFact).
const (
	TopicAuditAppended = "event.audit.appended.v1"
)

// AuditAppendedEvent is the payload for event.audit.appended.v1.
// Emitted by auditappend after a source event is folded into the hash chain.
type AuditAppendedEvent struct {
	AuditEntryID string `json:"auditEntryId"`
	EventType    string `json:"eventType"`
}
