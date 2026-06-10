package domain

// Event topic constants for configcore. Shared across slices to prevent
// duplicate declarations (configwrite, configpublish, configsubscribe).
//
// PR-A6 split the former config action-discriminator topic into semantically
// distinct topics:
//   - TopicConfigEntryUpserted    — current config state after create/update/rollback
//   - TopicConfigEntryDeleted     — current config state after delete
//   - TopicConfigVersionPublished — versioned snapshot creation (configpublish)
//
// This eliminates action-discriminator schemas and lets each subscriber listen
// only to the event kind it can actually apply.
const (
	TopicConfigEntryUpserted    = "event.config.entry-upserted.v1"
	TopicConfigEntryDeleted     = "event.config.entry-deleted.v1"
	TopicConfigVersionPublished = "event.config.version-published.v1"
	TopicConfigRollback         = "event.config.rollback.v1"
)
