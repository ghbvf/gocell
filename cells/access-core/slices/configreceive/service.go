// Package configreceive implements the config-receive slice: consumes
// config change events from config-core. Currently logs changes for
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
	// TopicConfigChanged is the event topic consumed by this slice.
	TopicConfigChanged = "event.config.changed.v1"
)

// Service consumes config change events for access-core.
//
// Consumer: cg-access-core-config-changed
// Idempotency: log-only (no side effects), inherently idempotent
// Disposition: Ack on success / Reject on permanent unmarshal error
type Service struct {
	logger *slog.Logger
}

// NewService creates a config-receive Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{logger: logger}
}

// HandleEvent processes a config change event. This is the callback registered
// with the event bus subscriber via outbox.WrapLegacyHandler.
func (s *Service) HandleEvent(_ context.Context, entry outbox.Entry) error {
	var event struct {
		Action string `json:"action"`
		Key    string `json:"key"`
		Value  string `json:"value"`
	}

	if err := json.Unmarshal(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return fmt.Errorf("config-receive: unmarshal payload: %w", err)
	}

	switch event.Action {
	case "created", "updated":
		s.logger.Info("config-receive: config changed",
			slog.String("key", event.Key), slog.String("action", event.Action))
	case "deleted":
		s.logger.Info("config-receive: config deleted",
			slog.String("key", event.Key))
	case "published":
		s.logger.Info("config-receive: config published (no action)",
			slog.String("key", event.Key))
	default:
		s.logger.Warn("config-receive: unknown action, skipping",
			slog.String("key", event.Key), slog.String("action", event.Action))
	}

	return nil
}
