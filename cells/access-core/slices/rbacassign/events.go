package rbacassign

// Topic constants for RBAC role-change events (L2 OutboxFact).
// These topics are published atomically with the role mutation inside
// a single DB transaction (via outbox.Writer + persistence.TxRunner).
//
// Consumers: sessionlogout.Consumer (access-core), audit-core.
const (
	TopicRoleAssigned = "event.role.assigned.v1"
	TopicRoleRevoked  = "event.role.revoked.v1"
	ActionAssigned    = "assigned"
	ActionRevoked     = "revoked"
)

// RoleChangedEvent is the payload for both event.role.assigned.v1 and event.role.revoked.v1.
// JSON fields use camelCase per cell-patterns.md (new events — not the legacy snake_case events).
type RoleChangedEvent struct {
	UserID string `json:"userId"`
	RoleID string `json:"roleId"`
	// Action is either ActionAssigned or ActionRevoked.
	Action string `json:"action"`
}
