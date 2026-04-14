// Package configsubscribe implements the config-subscribe slice: consumes
// config change events to update a local cache.
package configsubscribe

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TopicConfigChanged is re-exported from domain for this package's callers.
const TopicConfigChanged = domain.TopicConfigChanged

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
		cache: &Cache{values: make(map[string]string)},
		logger: logger,
	}
}

// Cache returns the local config cache for reading.
func (s *Service) Cache() *Cache {
	return s.cache
}

// HandleEvent processes a config change event. This is the callback registered
// with the event bus subscriber.
//
// Consumer: cg-config-core-config-changed
// Idempotency key: N/A (in-memory cache, idempotent by nature)
// ACK timing: after cache update
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
func (s *Service) HandleEvent(_ context.Context, entry outbox.Entry) error {
	var event struct {
		Action string `json:"action"`
		Key    string `json:"key"`
		Value  string `json:"value"`
	}

	if err := json.Unmarshal(entry.Payload, &event); err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		// Permanent error: return error so ConsumerBase routes to dead letter
		// after exhausting retries.
		return fmt.Errorf("config-subscribe: unmarshal payload: %w", err)
	}

	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()

	switch event.Action {
	case "deleted":
		delete(s.cache.values, event.Key)
		s.logger.Info("config-subscribe: key deleted from cache",
			slog.String("key", event.Key))
	case "created", "updated":
		s.cache.values[event.Key] = event.Value
		s.logger.Info("config-subscribe: cache updated",
			slog.String("key", event.Key), slog.String("action", event.Action))
	case "published":
		// Published events carry config_id+version but no value — skip cache
		// update to avoid overwriting with empty string. The actual value is
		// already in cache from the preceding created/updated event.
		s.logger.Info("config-subscribe: published event (no cache update)",
			slog.String("key", event.Key))
	default:
		s.logger.Warn("config-subscribe: unknown action, skipping",
			slog.String("key", event.Key), slog.String("action", event.Action))
	}

	return nil
}
