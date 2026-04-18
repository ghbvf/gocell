package dto

// Topic constants and payload for RBAC role-change events (L2 OutboxFact).
//
// These events are published atomically with the role mutation inside a single
// DB transaction (via outbox.Writer + persistence.TxRunner). Consumers include
// access-core's sessionlogout.Consumer (invalidates sessions) and audit-core
// (appends to the hash chain).
//
// Lives in internal/dto (not in slices/rbacassign) because both the rbacassign
// producer slice and the sessionlogout consumer slice must share the type —
// per cell-patterns.md, cross-slice DTOs belong in internal/dto/.
const (
	TopicRoleAssigned = "event.role.assigned.v1"
	TopicRoleRevoked  = "event.role.revoked.v1"
	ActionAssigned    = "assigned"
	ActionRevoked     = "revoked"
)

// RoleChangedEvent is the payload for both event.role.assigned.v1 and
// event.role.revoked.v1. JSON fields use camelCase per cell-patterns.md.
type RoleChangedEvent struct {
	UserID string `json:"userId"`
	RoleID string `json:"roleId"`
	// Action is either ActionAssigned or ActionRevoked.
	Action string `json:"action"`
}
