// Package domain contains the audit-core Cell domain models.
package domain

import "time"

// AuditEntry is an immutable record in the tamper-evident audit log.
type AuditEntry struct {
	ID        string
	EventID   string
	EventType string
	ActorID   string
	Timestamp time.Time
	Payload   []byte
	PrevHash  string
	Hash      string
}
