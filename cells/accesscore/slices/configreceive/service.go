// Package configreceive implements the config-receive slice: consumes
// config state-sync events from configcore. Currently logs changes for
// observability; future use: refresh JWT TTL, key rotation intervals, etc.
package configreceive

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

const (
	// TopicConfigEntryUpserted is the config state-sync topic consumed by this slice.
	TopicConfigEntryUpserted = "event.config.entry-upserted.v1"
	// TopicConfigEntryDeleted is the config delete state-sync topic consumed by this slice.
	TopicConfigEntryDeleted = "event.config.entry-deleted.v1"
)

// Service consumes config change events for accesscore.
//
// Consumer: cg-accesscore-config-events
// Idempotency: log-only (no side effects), inherently idempotent
// Disposition: Ack on success / Reject on permanent unmarshal or semantic error
// DLX: broker-native via DispositionReject → Nack(requeue=false)
type Service struct {
	logger *slog.Logger
}

// NewService creates a config-receive Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{logger: logger}
}

// HandleEntryUpserted processes an event.config.entry-upserted.v1 event.
func (s *Service) HandleEntryUpserted(_ context.Context, entry outbox.Entry) error {
	event, err := dto.DecodeEntryUpserted(entry.Payload)
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-upserted payload: %w", err))
	}

	s.logger.Info("config-receive: config upserted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(_ context.Context, entry outbox.Entry) error {
	event, err := dto.DecodeEntryDeleted(entry.Payload)
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-deleted payload: %w", err))
	}

	s.logger.Info("config-receive: config deleted", slog.String("key", event.Key))
	return nil
}
