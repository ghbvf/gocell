package dto

// Topic constants for user-lifecycle events (L2 OutboxFact).
//
// Shared between accesscore slices that produce user events (identitymanage,
// setup) so there's a single source of truth. Downstream consumers in other
// cells (auditcore) match against these topic strings directly via wire-level
// string literals — the dto package is accesscore-internal and must not be
// imported by other cells (CLAUDE.md 分层依赖规则).
const (
	TopicUserCreated = "event.user.created.v1"
	TopicUserLocked  = "event.user.locked.v1"
)

// UserCreatedEvent is the payload for event.user.created.v1.
//
// Wire format uses snake_case to match the frozen event.user.created.v1 schema
// (contracts/event/user/created/v1/payload.schema.json). New event contracts
// should use camelCase per cell-patterns.md; this schema is grandfathered until
// the v1.0 post-release migration.
type UserCreatedEvent struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}
