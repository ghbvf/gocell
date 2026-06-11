package dto

// TopicPolicyUpdated is the outbox topic for event.policy.updated.v1.
// Emitted by the policymanage slice on every create, update, and delete
// mutation (consistency level L2: local transaction + outbox publish).
const TopicPolicyUpdated = "event.policy.updated.v1"

// Policy action constants for event.policy.updated.v1.
// Subscribers use the action field to distinguish create/update/delete
// without needing to fetch the policy (metadata-only event bus principle).
const (
	// PolicyActionCreated is the action code emitted when a policy is created.
	PolicyActionCreated = "created"
	// PolicyActionUpdated is the action code emitted when a policy is updated.
	PolicyActionUpdated = "updated"
	// PolicyActionDeleted is the action code emitted when a policy is deleted.
	PolicyActionDeleted = "deleted"
)

// PolicyUpdated is the event.policy.updated.v1 payload.
// Metadata-only: no rule bodies on the bus. Subscribers MUST refetch
// via GET /api/v1/access/policies/{id} to obtain the full policy state.
// Version is included so consumers can detect gaps and tombstones.
//
// ref: event.policy.updated.v1 payload.schema.json
type PolicyUpdated struct {
	PolicyID string `json:"policyId"`
	Version  int    `json:"version"`
	Action   string `json:"action"`
	ActorID  string `json:"actorId"`
}
