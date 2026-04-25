// Package configsubscribe implements the config-subscribe slice: consumes
// config state-sync events to update a local version-tracking cache.
//
// Metadata-only model: event.config.entry-upserted.v1 and
// event.config.entry-deleted.v1 carry only key+version.
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

// cacheEntry tracks the highest version seen for a config key plus a presence
// flag indicating whether the key is currently active (present=true) or
// tombstoned by a delete event (present=false).
//
// Design invariant: version is monotonically non-decreasing — it is NEVER
// reset or decremented, not even on delete. This means a replayed older upsert
// (at-least-once delivery) arriving after a delete will be rejected because
// event.Version <= tombstone.version.
//
// Memory note: tombstone entries (present=false) are retained for the lifetime
// of the process so that the monotonic protection holds across replays. If
// process memory becomes a concern (e.g. high-churn keys) a TTL-based eviction
// or persistent tombstone store should be introduced — that is out of scope for
// this PR.
type cacheEntry struct {
	version int  // highest version seen, never decremented
	present bool // false = tombstoned by a delete event
}

// Cache tracks the latest known version and presence for each config key
// observed from events.
// It does NOT store values — subscribers must refetch via GET /api/v1/config/{key}.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

// GetVersion returns the last known version for a key and whether the entry is
// currently active (present=true).  present=false means the key was deleted
// (tombstoned); the version returned is the tombstone version.
func (c *Cache) GetVersion(key string) (version int, present bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return 0, false
	}
	return e.version, e.present
}

// Len returns the number of active (present=true) entries.
// Tombstoned entries are excluded to avoid the count growing unboundedly
// with deleted keys.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, e := range c.entries {
		if e.present {
			n++
		}
	}
	return n
}

// Service consumes config change events and maintains a local version-tracking cache.
type Service struct {
	cache  *Cache
	logger *slog.Logger
}

// NewService creates a config-subscribe Service.
func NewService(logger *slog.Logger) *Service {
	return &Service{
		cache:  &Cache{entries: make(map[string]cacheEntry)},
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
// (including versions <= a tombstone version) are silently dropped.
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
	known := s.cache.entries[event.Key]
	if event.Version <= known.version {
		s.cache.mu.Unlock()
		s.logger.Debug("config-subscribe: stale or replayed entry-upserted ignored",
			slog.String("key", event.Key),
			slog.Int("incoming_version", event.Version),
			slog.Int("known_version", known.version))
		return nil
	}
	s.cache.entries[event.Key] = cacheEntry{version: event.Version, present: true}
	s.cache.mu.Unlock()
	s.logger.Debug("config-subscribe: cache updated",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
//
// Tombstone model: instead of deleting the cache entry, we record a tombstone
// (present=false) at the deleted version. This preserves monotonic protection:
// a replayed older upsert arriving after the delete will be rejected because
// event.Version <= tombstone.version.
//
// Stale-delete guard: if event.Version <= known.version the delete event itself
// is stale/replayed and is dropped without modifying the cache. This prevents
// an old delete event from overwriting a newer upsert that arrived in between.
//
// Consumer: cg-configcore-entry-deleted
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false)
func (s *Service) HandleEntryDeleted(_ context.Context, entry outbox.Entry) error {
	event, err := configevents.DecodeEntryDeleted(entry.Payload)
	if err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-deleted payload: %w", err))
	}

	s.cache.mu.Lock()
	known := s.cache.entries[event.Key]
	// Stale-delete guard: drop if event.Version < known.version.
	// A delete at version V must be accepted when V >= known.version:
	//   - V == known.version: this is the delete of the currently known entry.
	//   - V > known.version: a newer delete (e.g. key re-created and deleted again).
	// Only V < known.version is truly stale (a delete that predates a newer upsert).
	if event.Version < known.version {
		s.cache.mu.Unlock()
		s.logger.Debug("config-subscribe: stale entry-deleted ignored (predates a newer upsert)",
			slog.String("key", event.Key),
			slog.Int("incoming_version", event.Version),
			slog.Int("known_version", known.version))
		return nil
	}
	s.cache.entries[event.Key] = cacheEntry{version: event.Version, present: false}
	s.cache.mu.Unlock()
	s.logger.Debug("config-subscribe: key tombstoned in cache",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	return nil
}
