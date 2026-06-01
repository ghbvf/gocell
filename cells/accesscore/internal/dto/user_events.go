package dto

// TopicBootstrapAuthFailed is the outbox topic for event.auth.bootstrap-failed.v1.
// Emitted by the setup slice when the bootstrap middleware rejects a request
// (missing header, wrong credentials, or rate limit exceeded).
const TopicBootstrapAuthFailed = "event.auth.bootstrap-failed.v1"

// BootstrapAuthFailedEvent is the payload for event.auth.bootstrap-failed.v1.
//
// Reason values mirror runtime/audit.ReasonMissingHeader / ReasonWrongCredentials
// / ReasonRateLimited constants (the runtime/audit constants are the authoritative
// names; dto keeps typed copies to remain runtime/audit-import-free from cells/).
type BootstrapAuthFailedEvent struct {
	Reason   string `json:"reason"`
	ClientIP string `json:"clientIp,omitempty"`
}

// Topic constants for user-lifecycle events (L2 OutboxFact).
//
// Shared between accesscore slices that produce user events (identitymanage,
// setup) so there's a single source of truth. Downstream consumers in other
// cells (auditcore) match against these topic strings directly via wire-level
// string literals — the dto package is accesscore-internal and must not be
// imported by other cells (CLAUDE.md 分层依赖规则).
const (
	TopicUserCreated  = "event.user.created.v1"
	TopicUserLocked   = "event.user.locked.v1"
	TopicUserDeleted  = "event.user.deleted.v1"
	TopicUserUpdated  = "event.user.updated.v1"
	TopicUserUnlocked = "event.user.unlocked.v1"
)

// UserCreatedEvent is the payload for event.user.created.v1.
//
// ActorID identifies the principal that triggered the create operation:
//   - identitymanage.Create: the admin's auth.FromContext(ctx).Subject
//   - setup.publishUserCreated: the sentinel "system" (first-run admin
//     bootstrap has no calling principal — there is no admin yet)
type UserCreatedEvent struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	ActorID  string `json:"actorId"`
}

// UserLockedEvent is the payload for event.user.locked.v1.
type UserLockedEvent struct {
	UserID  string `json:"userId"`
	ActorID string `json:"actorId"`
}

// UserDeletedEvent is the payload for event.user.deleted.v1.
type UserDeletedEvent struct {
	UserID  string `json:"userId"`
	ActorID string `json:"actorId"`
}

// UserUpdatedEvent is the payload for event.user.updated.v1.
type UserUpdatedEvent struct {
	UserID  string `json:"userId"`
	ActorID string `json:"actorId"`
}

// UserUnlockedEvent is the payload for event.user.unlocked.v1.
type UserUnlockedEvent struct {
	UserID  string `json:"userId"`
	ActorID string `json:"actorId"`
}
