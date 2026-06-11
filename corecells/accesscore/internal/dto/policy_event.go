package dto

// TopicPolicyUpdated is the outbox topic for event.policy.updated.v1.
// Emitted by the policymanage slice on every create, update, and delete
// mutation (consistency level L2: local transaction + outbox publish).
const TopicPolicyUpdated = "event.policy.updated.v1"
