// Package configsubscribe implements the config-subscribe slice: consumes
// config state-sync events to update a local cache.
package configsubscribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Re-exported from domain so external callers / tests can refer to the topic
// without importing internal/domain directly.
const (
	TopicConfigEntryUpserted = domain.TopicConfigEntryUpserted
	TopicConfigEntryDeleted  = domain.TopicConfigEntryDeleted
)

type configEntryUpsertedPayload struct {
	Key     string  `json:"key"`
	Value   *string `json:"value"`
	Version int     `json:"version"`
}

type configEntryDeletedPayload struct {
	Key string `json:"key"`
}

// Cache holds the latest config values observed from events.
type Cache struct {
	mu     sync.RWMutex
	values map[string]string
}

// Get returns the cached value for a key.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.values)
}

// Service consumes config change events and maintains a local cache.
type Service struct {
	cache  *Cache
	logger *slog.Logger
}

// NewService creates a config-subscribe Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{
		cache:  &Cache{values: make(map[string]string)},
		logger: logger,
	}
}

// Cache returns the local config cache for reading.
func (s *Service) Cache() *Cache {
	return s.cache
}

// HandleEntryUpserted processes an event.config.entry-upserted.v1 event.
//
// Consumer: cg-configcore-entry-upserted
// Idempotency key: N/A (in-memory cache, idempotent by nature)
// ACK timing: after cache update
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
func (s *Service) HandleEntryUpserted(_ context.Context, entry outbox.Entry) error {
	var event configEntryUpsertedPayload
	if err := decodeStrict(entry.Payload, &event); err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-upserted payload: %w", err))
	}
	if err := validateUpserted(event.Key, event.Value, event.Version); err != nil {
		s.logger.Warn("config-subscribe: invalid entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(err)
	}

	s.cache.mu.Lock()
	s.cache.values[event.Key] = *event.Value
	s.cache.mu.Unlock()
	s.logger.Info("config-subscribe: cache updated",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(_ context.Context, entry outbox.Entry) error {
	var event configEntryDeletedPayload
	if err := decodeStrict(entry.Payload, &event); err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-deleted payload: %w", err))
	}
	if strings.TrimSpace(event.Key) == "" {
		err := fmt.Errorf("config-subscribe: entry-deleted missing key")
		s.logger.Warn("config-subscribe: invalid entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(err)
	}

	s.cache.mu.Lock()
	delete(s.cache.values, event.Key)
	s.cache.mu.Unlock()
	s.logger.Info("config-subscribe: key deleted from cache",
		slog.String("key", event.Key))
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
		return fmt.Errorf("config-subscribe: entry-upserted missing key")
	}
	if value == nil {
		return fmt.Errorf("config-subscribe: entry-upserted missing value for key %q", key)
	}
	if version < 1 {
		return fmt.Errorf("config-subscribe: entry-upserted invalid version %d for key %q", version, key)
	}
	return nil
}
