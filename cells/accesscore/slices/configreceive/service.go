// Package configreceive implements the config-receive slice: consumes
// config change events from configcore. Currently logs changes for
// observability; future use: refresh JWT TTL, key rotation intervals, etc.
package configreceive

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/outbox"
)

const (
	// TopicConfigEntryWritten is the entry-write topic consumed by this slice.
	TopicConfigEntryWritten = "event.config.entry-written.v1"
	// TopicConfigVersionPublished is the version-snapshot topic consumed by this slice.
	TopicConfigVersionPublished = "event.config.version-published.v1"
)

// ConfigEntryWrittenAction enumerates the CRUD actions the entry-written event
// can carry. Duplicated locally instead of imported from configcore's
// internal/domain — cross-cell internal imports are forbidden by CLAUDE.md.
// Kept in lockstep with the producer-side enum; the wire contract (see
// contracts/event/config/entry-written/v1/payload.schema.json) is the actual
// source of truth for both sides.
type ConfigEntryWrittenAction string

const (
	configEntryActionCreated ConfigEntryWrittenAction = "created"
	configEntryActionUpdated ConfigEntryWrittenAction = "updated"
	configEntryActionDeleted ConfigEntryWrittenAction = "deleted"
)

// ConfigEntryWrittenEvent is the payload for event.config.entry-written.v1.
// Mirrors configcore/internal/domain/config_events.go shape without importing
// another cell's internal/ — cross-cell imports are forbidden by CLAUDE.md.
type ConfigEntryWrittenEvent struct {
	Action  ConfigEntryWrittenAction `json:"action"`
	Key     string                   `json:"key"`
	Value   string                   `json:"value,omitempty"`
	Version int                      `json:"version,omitempty"`
}

// ConfigVersionPublishedEvent is the payload for event.config.version-published.v1.
type ConfigVersionPublishedEvent struct {
	Key       string `json:"key"`
	ConfigID  string `json:"configId"`
	Version   int    `json:"version"`
	Sensitive bool   `json:"sensitive"`
}

// Service consumes config change events for accesscore.
//
// Consumer: cg-accesscore-config-events
// Idempotency: log-only (no side effects), inherently idempotent
// Disposition: Ack on success / Reject on permanent unmarshal error
// DLX: broker-native via DispositionReject → Nack(requeue=false)
type Service struct {
	logger *slog.Logger
}

// NewService creates a config-receive Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{logger: logger}
}

// HandleEntryWritten processes an event.config.entry-written.v1 event.
func (s *Service) HandleEntryWritten(_ context.Context, entry outbox.Entry) error {
	var event ConfigEntryWrittenEvent

	if err := json.Unmarshal(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-written event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-written payload: %w", err))
	}

	switch event.Action {
	case configEntryActionCreated, configEntryActionUpdated:
		s.logger.Info("config-receive: config changed",
			slog.String("key", event.Key), slog.String("action", string(event.Action)))
	case configEntryActionDeleted:
		s.logger.Info("config-receive: config deleted",
			slog.String("key", event.Key))
	default:
		// Fail-closed: unknown actions are permanent errors routed to DLX.
		//
		// ref: K8s workqueue fail-closed semantics; Watermill Nack on unknown type
		s.logger.Warn("config-receive: unknown action, routing to dead letter",
			slog.String("action", string(event.Action)), slog.String("key", event.Key))
		return outbox.NewPermanentError(
			fmt.Errorf("unknown action %q for key %q", event.Action, event.Key),
		)
	}

	return nil
}

// HandleVersionPublished processes an event.config.version-published.v1 event.
func (s *Service) HandleVersionPublished(_ context.Context, entry outbox.Entry) error {
	var event ConfigVersionPublishedEvent

	if err := json.Unmarshal(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal version-published event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal version-published payload: %w", err))
	}

	s.logger.Info("config-receive: config version published",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}
