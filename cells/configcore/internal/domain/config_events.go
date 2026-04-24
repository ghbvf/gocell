package domain

// Event payload structs for configcore (L2 OutboxFact).
//
// JSON field names are camelCase per cell-patterns.md (HTTP DTO 和事件 payload
// 统一 camelCase). The previous snake_case fields (config_id, target_version,
// new_version) were retired as part of PR-A6's full-break sweep.
//
// Structs live in internal/domain alongside topics.go so a single import gives
// producer slices both the wire shape and the routing key — the configcore
// analogue of accesscore's internal/dto/session_events.go.

// ConfigEntryWrittenAction enumerates the CRUD actions on a config entry.
type ConfigEntryWrittenAction string

const (
	ConfigEntryActionCreated ConfigEntryWrittenAction = "created"
	ConfigEntryActionUpdated ConfigEntryWrittenAction = "updated"
	ConfigEntryActionDeleted ConfigEntryWrittenAction = "deleted"
)

// ConfigEntryWrittenEvent is the payload for event.config.entry-written.v1.
// Produced by configwrite on Create / Update / Delete.
type ConfigEntryWrittenEvent struct {
	Action  ConfigEntryWrittenAction `json:"action"`
	Key     string                   `json:"key"`
	Value   string                   `json:"value,omitempty"`
	Version int                      `json:"version,omitempty"`
}

// ConfigVersionPublishedEvent is the payload for
// event.config.version-published.v1. Produced by configpublish.Publish.
// No `action` field — topic name carries the semantic.
type ConfigVersionPublishedEvent struct {
	Key       string `json:"key"`
	ConfigID  string `json:"configId"`
	Version   int    `json:"version"`
	Sensitive bool   `json:"sensitive"`
}

// ConfigRollbackEvent is the payload for event.config.rollback.v1.
// Produced by configpublish.Rollback. No `action` field — topic name is
// tautological with the former "action": "rollback" discriminator.
type ConfigRollbackEvent struct {
	Key           string `json:"key"`
	TargetVersion int    `json:"targetVersion"`
	NewVersion    int    `json:"newVersion"`
}
