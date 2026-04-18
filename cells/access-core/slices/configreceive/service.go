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

// ConfigChangedEvent is the payload for event.config.changed.v1.
// Matches contracts/event/config/changed/v1/payload.schema.json.
type ConfigChangedEvent struct {
	Action   string `json:"action"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	Version  int    `json:"version,omitempty"`
	ConfigID string `json:"config_id,omitempty"`
}

// Service consumes config change events for access-core.
//
// Consumer: cg-access-core-config-changed
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

// HandleEvent processes a config change event. This is the callback registered
// with the event bus subscriber via outbox.WrapLegacyHandler.
func (s *Service) HandleEvent(_ context.Context, entry outbox.Entry) error {
	var event ConfigChangedEvent

	if err := json.Unmarshal(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal payload: %w", err))
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
		// Fail-closed: unknown actions are permanent errors routed to DLX.
		//
		// ref: K8s workqueue fail-closed semantics; Watermill Nack on unknown type
		s.logger.Warn("config-receive: unknown action, routing to dead letter",
			slog.String("action", event.Action), slog.String("key", event.Key))
		return outbox.NewPermanentError(
			fmt.Errorf("unknown action %q for key %q", event.Action, event.Key),
		)
	}

	return nil
}
