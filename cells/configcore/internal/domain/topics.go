package domain

// Event topic constants for configcore. Shared across slices to prevent
// duplicate declarations (configwrite, configpublish, configsubscribe).
//
// PR-A6 split the former event.config.changed.v1 into two semantically
// distinct topics:
//   - TopicConfigEntryWritten     — CRUD on a config entry (configwrite)
//   - TopicConfigVersionPublished — versioned snapshot creation (configpublish)
//
// This eliminates the action-oneOf schema the previous single topic used and
// lets each subscriber listen to only the half it cares about.
const (
	TopicConfigEntryWritten     = "event.config.entry-written.v1"
	TopicConfigVersionPublished = "event.config.version-published.v1"
	TopicConfigRollback         = "event.config.rollback.v1"
	TopicFlagChanged            = "event.flag.changed.v1"
)
