// Package configsubscribe implements the config-subscribe slice: consumes
// config state-sync events to update a local version-tracking cache.
//
// Metadata-only model: event.config.entry-upserted.v1 carries only key+version.
// Subscribers MUST refetch via GET /api/v1/config/{key} to obtain the value.
// ref: NATS subject+bytes / Watermill payload-bytes boundary.
package configsubscribe

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Cache tracks the latest known version for each config key observed from events.
// It does NOT store values — subscribers must refetch via GET /api/v1/config/{key}.
//
// versions only, no cached values; subscribers MUST refetch via GET for the actual value.
type Cache struct {
	mu       sync.RWMutex
	versions map[string]int
}

// GetVersion returns the last known version for a key.
func (c *Cache) GetVersion(key string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.versions[key]
	return v, ok
}

// Len returns the number of tracked keys.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.versions)
}

// Service consumes config change events and maintains a local version-tracking cache.
type Service struct {
	cache  *Cache
	logger *slog.Logger
}

// NewService creates a config-subscribe Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{
		cache:  &Cache{versions: make(map[string]int)},
		logger: logger,
	}
}

// Cache returns the local config cache for reading.
func (s *Service) Cache() *Cache {
	return s.cache
}

// HandleEntryUpserted processes an event.config.entry-upserted.v1 event.
// Records the known version for the key; does not store a value.
// Callers wanting the current value must refetch via GET /api/v1/config/{key}.
//
// Version monotonicity: events with a version <= the known version for the key
// are silently dropped (stale or replayed entry).
//
// Consumer: cg-configcore-entry-upserted
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false)
func (s *Service) HandleEntryUpserted(_ context.Context, entry outbox.Entry) error {
	event, err := configevents.DecodeEntryUpserted(entry.Payload)
	if err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-upserted payload: %w", err))
	}

	s.cache.mu.Lock()
	known := s.cache.versions[event.Key]
	if event.Version <= known {
		s.cache.mu.Unlock()
		s.logger.Debug("config-subscribe: stale or replayed entry-upserted ignored",
			slog.String("key", event.Key),
			slog.Int("incoming_version", event.Version),
			slog.Int("known_version", known))
		return nil
	}
	s.cache.versions[event.Key] = event.Version
	s.cache.mu.Unlock()
	s.logger.Info("config-subscribe: cache updated",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(_ context.Context, entry outbox.Entry) error {
	event, err := configevents.DecodeEntryDeleted(entry.Payload)
	if err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-deleted payload: %w", err))
	}

	s.cache.mu.Lock()
	_, existed := s.cache.versions[event.Key]
	delete(s.cache.versions, event.Key)
	s.cache.mu.Unlock()

	if existed {
		s.logger.Info("config-subscribe: key deleted from cache",
			slog.String("key", event.Key))
	} else {
		s.logger.Debug("config-subscribe: delete event for unknown key (idempotent)",
			slog.String("key", event.Key))
	}
	return nil
}
