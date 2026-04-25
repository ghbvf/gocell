package domain

import "github.com/ghbvf/gocell/cells/configcore/internal/events"

// Event payload structs for configcore (L2 OutboxFact).
//
// JSON field names are camelCase per cell-patterns.md (HTTP DTO 和事件 payload
// 统一 camelCase). The previous snake_case fields (config_id, target_version,
// new_version) were retired as part of PR-A6's full-break sweep.
//
// ConfigEntryUpsertedEvent is metadata-only: subscribers MUST refetch via
// GET /api/v1/config/{key} to obtain the value.
// ref: NATS subject+bytes / Watermill payload-bytes boundary.

// ConfigEntryUpsertedEvent is the metadata-only payload for event.config.entry-upserted.v1.
type ConfigEntryUpsertedEvent = events.EntryUpserted

// ConfigEntryDeletedEvent is the payload for event.config.entry-deleted.v1.
type ConfigEntryDeletedEvent = events.EntryDeleted

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
