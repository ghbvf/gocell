// Package configreceive implements the config-receive slice: consumes
// config state-sync events from configcore. Currently logs changes for
// observability; future use: refresh JWT TTL, key rotation intervals, etc.
package configreceive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/ghbvf/gocell/kernel/outbox"
)

const (
	// TopicConfigEntryUpserted is the config state-sync topic consumed by this slice.
	TopicConfigEntryUpserted = "event.config.entry-upserted.v1"
	// TopicConfigEntryDeleted is the config delete state-sync topic consumed by this slice.
	TopicConfigEntryDeleted = "event.config.entry-deleted.v1"
)

// ConfigEntryUpsertedEvent is the payload for event.config.entry-upserted.v1.
// Mirrors configcore/internal/domain/config_events.go shape without importing
// another cell's internal/ — cross-cell internal imports are forbidden by CLAUDE.md.
type ConfigEntryUpsertedEvent struct {
	Key     string  `json:"key"`
	Value   *string `json:"value"`
	Version int     `json:"version"`
}

// ConfigEntryDeletedEvent is the payload for event.config.entry-deleted.v1.
type ConfigEntryDeletedEvent struct {
	Key string `json:"key"`
}

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
	var event ConfigEntryUpsertedEvent
	if err := decodeStrict(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-upserted payload: %w", err))
	}
	if err := validateUpserted(event.Key, event.Value, event.Version); err != nil {
		s.logger.Warn("config-receive: invalid entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(err)
	}

	s.logger.Info("config-receive: config upserted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(_ context.Context, entry outbox.Entry) error {
	var event ConfigEntryDeletedEvent
	if err := decodeStrict(entry.Payload, &event); err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-deleted payload: %w", err))
	}
	if strings.TrimSpace(event.Key) == "" {
		err := fmt.Errorf("config-receive: entry-deleted missing key")
		s.logger.Warn("config-receive: invalid entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(err)
	}

	s.logger.Info("config-receive: config deleted", slog.String("key", event.Key))
	return nil
}

func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values in payload")
		}
		return err
	}
	return nil
}

func validateUpserted(key string, value *string, version int) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("config-receive: entry-upserted missing key")
	}
	if value == nil {
		return fmt.Errorf("config-receive: entry-upserted missing value for key %q", key)
	}
	if version < 1 {
		return fmt.Errorf("config-receive: entry-upserted invalid version %d for key %q", version, key)
	}
	return nil
}
