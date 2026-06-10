package dto

// Topic constants for session-lifecycle events (L2 OutboxFact).
//
// Co-located with the typed payload structs so a single import gives the
// producer slice both the wire-level routing key and the wire shape, keeping
// them in lockstep when the schema evolves.
const (
	TopicSessionCreated = "event.session.created.v1"
	TopicSessionRevoked = "event.session.revoked.v1"
)

// SessionCreatedEvent is the payload for event.session.created.v1.
//
// JSON field names are camelCase per cell-patterns.md (HTTP DTO 和事件 payload
// 统一 camelCase). The previous snake_case fields (session_id / user_id) were
// retired as part of PR-A6's "彻底 / no back-compat" sweep.
type SessionCreatedEvent struct {
	SessionID string `json:"sessionId"`
	UserID    string `json:"userId"`
}

// SessionRevokedEvent is the payload for event.session.revoked.v1.
type SessionRevokedEvent struct {
	SessionID string `json:"sessionId"`
	UserID    string `json:"userId"`
}
