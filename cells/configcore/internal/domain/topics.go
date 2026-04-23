package domain

// Event topic constants for configcore. Shared across slices to prevent
// duplicate declarations (configwrite, configpublish, configsubscribe).
const (
	TopicConfigChanged  = "event.config.changed.v1"
	TopicConfigRollback = "event.config.rollback.v1"
	TopicFlagChanged    = "event.flag.changed.v1"
)
