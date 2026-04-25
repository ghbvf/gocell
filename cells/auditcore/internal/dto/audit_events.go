// Package dto holds typed payload structs for auditcore event contracts.
//
// JSON field names are camelCase per cell-patterns.md (HTTP DTO 和事件 payload
// 统一 camelCase). The previous snake_case fields on event.audit.appended.v1
// (audit_entry_id / event_type) were retired as part of PR-A6's full-break
// sweep.
package dto

// Topic constants for auditcore events (L2 OutboxFact).
const (
	TopicAuditAppended          = "event.audit.appended.v1"
	TopicAuditIntegrityVerified = "event.audit.integrity-verified.v1"
)

// AuditAppendedEvent is the payload for event.audit.appended.v1.
// Emitted by auditappend after a source event is folded into the hash chain.
type AuditAppendedEvent struct {
	AuditEntryID string `json:"auditEntryId"`
	EventType    string `json:"eventType"`
}

// AuditIntegrityVerifiedEvent is the payload for
// event.audit.integrity-verified.v1. Emitted by auditverify after a chain
// range is verified.
type AuditIntegrityVerifiedEvent struct {
	Valid             bool `json:"valid"`
	FirstInvalidIndex int  `json:"firstInvalidIndex"`
	EntriesChecked    int  `json:"entriesChecked"`
}
