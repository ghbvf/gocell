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

// ConfigEntryUpsertedEvent is the payload for event.config.entry-upserted.v1.
// Produced by configwrite on Create / Update and by configpublish.Rollback
// after restoring the live entry to the rollback snapshot.
type ConfigEntryUpsertedEvent struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version int    `json:"version"`
}

// ConfigEntryDeletedEvent is the payload for event.config.entry-deleted.v1.
// Produced by configwrite on Delete.
type ConfigEntryDeletedEvent struct {
	Key string `json:"key"`
}

// ConfigVersionPublishedEvent is the payload for
// event.config.version-published.v1. Produced by configpublish.Publish.
// No `action` field — topic name carries the semantic.
type ConfigVersionPublishedEvent struct {
	Key      string `json:"key"`
	ConfigID string `json:"configId"`
	Version  int    `json:"version"`
}

// ConfigRollbackEvent is the payload for event.config.rollback.v1.
// Produced by configpublish.Rollback. No `action` field — topic name is
// tautological with the former "action": "rollback" discriminator.
type ConfigRollbackEvent struct {
	Key           string `json:"key"`
	TargetVersion int    `json:"targetVersion"`
	NewVersion    int    `json:"newVersion"`
}
