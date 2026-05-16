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
	"time"

	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// defaultTombstoneTTL is the minimum safe tombstone retention period. It MUST
// be ≥ the at-least-once redelivery / Claimer idempotency window (also 24h).
// GC'ing a tombstone before the replay window closes would let a stale replayed
// upsert bypass the monotonic guard and incorrectly resurrect a deleted key.
const defaultTombstoneTTL = 24 * time.Hour

// cacheCellID and cacheSliceID are the metric label values for the configsubscribe
// Cache. Cache is service-private — configsubscribe is the only owner.
const (
	cacheCellID  = "configcore"
	cacheSliceID = "configsubscribe"
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
// Tombstone TTL: tombstone entries (present=false) are TTL-reaped by the
// lifecycle-bound GC sweep (sweepTombstones / StartTombstoneGC). The hard
// invariant is tombstoneTTL ≥ Claimer idempotency window (24h): premature
// tombstone GC would allow a stale replayed upsert to bypass the monotonic
// guard and incorrectly resurrect a deleted key beyond the replay window.
// Active entries are never LRU-evicted; their count is bounded by the live
// config keyspace and the monotonic guard is fully preserved.
type cacheEntry struct {
	version   int       // highest version seen, never decremented
	present   bool      // false = tombstoned by a delete event
	deletedAt time.Time // non-zero only for tombstones (present=false)
}

// Cache tracks the latest known version and presence for each config key
// observed from events.
// It does NOT store values — subscribers must refetch via GET /api/v1/config/{key}.
//
// Tombstone TTL GC: sweepTombstones removes tombstone entries whose age since
// deletedAt exceeds tombstoneTTL. Active entries are never evicted — the
// monotonic-version guard for live keys is fully preserved.
type Cache struct {
	mu             sync.RWMutex
	entries        map[string]cacheEntry
	clk            clock.Clock
	tombstoneTTL   time.Duration
	cacheCollector obmetrics.EventbusCacheCollector
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

// sweepTombstones removes tombstone entries (present=false) whose age since
// deletedAt exceeds tombstoneTTL. Active entries are never touched — the
// monotonic-version guard for live keys is fully preserved. Each evicted
// tombstone increments eventbus_cache_tombstone_evicted_total.
func (c *Cache) sweepTombstones(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !e.present && now.Sub(e.deletedAt) > c.tombstoneTTL {
			delete(c.entries, k)
			c.cacheCollector.RecordTombstoneEvicted(cacheCellID, cacheSliceID)
		}
	}
}

// Service consumes config change events and maintains a local version-tracking cache.
type Service struct {
	cache                *Cache
	logger               *slog.Logger
	configEventCollector obmetrics.ConfigEventCollector
	clk                  clock.Clock

	// GC lifecycle state (protected by gcMu).
	gcMu      sync.Mutex
	gcStarted bool
	gcCancel  context.CancelFunc
	gcDone    chan struct{}
}

// Option configures a configsubscribe Service.
type Option func(*Service)

// WithConfigEventCollector injects config event process metrics.
func WithConfigEventCollector(c obmetrics.ConfigEventCollector) Option {
	return func(s *Service) {
		if c == nil {
			c = obmetrics.NoopConfigEventCollector{}
		}
		s.configEventCollector = c
	}
}

// WithClock injects a custom clock (e.g. clockmock.FakeClock) for testing.
// A nil clock is silently ignored; the default clock.Real() is used instead.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		if clk == nil {
			return
		}
		s.clk = clk
	}
}

// WithTombstoneTTL sets the tombstone TTL used by the background GC sweep.
// A non-positive value is silently treated as defaultTombstoneTTL (24h).
// Values > 0 but < defaultTombstoneTTL are accepted but trigger a Warn log
// because they weaken the monotonic replay protection.
func WithTombstoneTTL(d time.Duration) Option {
	return func(s *Service) {
		s.cache.tombstoneTTL = d // stored raw; normalization happens in NewService
	}
}

// WithEventbusCacheCollector injects the eventbus cache metrics collector.
// A nil collector is silently replaced with NoopEventbusCacheCollector{}.
func WithEventbusCacheCollector(c obmetrics.EventbusCacheCollector) Option {
	return func(s *Service) {
		if c == nil {
			c = obmetrics.NoopEventbusCacheCollector{}
		}
		s.cache.cacheCollector = c
	}
}

// NewService creates a config-subscribe Service.
func NewService(logger *slog.Logger, opts ...Option) *Service {
	clk := clock.Real()
	s := &Service{
		cache: &Cache{
			entries:        make(map[string]cacheEntry),
			clk:            clk,
			tombstoneTTL:   0, // will be normalized below
			cacheCollector: obmetrics.NoopEventbusCacheCollector{},
		},
		logger:               logger,
		configEventCollector: obmetrics.NoopConfigEventCollector{},
		clk:                  clk,
	}
	for _, o := range opts {
		o(s)
	}

	// Keep cache.clk in sync with the service-level clk (options may have changed it).
	s.cache.clk = s.clk

	// TTL normalization.
	ttl := s.cache.tombstoneTTL
	switch {
	case ttl <= 0:
		s.cache.tombstoneTTL = defaultTombstoneTTL
	case ttl < defaultTombstoneTTL:
		s.logger.Warn("config-subscribe: tombstoneTTL below Claimer idempotency window weakens monotonic replay protection",
			slog.Duration("tombstone_ttl", ttl),
			slog.Duration("min_recommended", defaultTombstoneTTL))
		// keep the explicit ttl
	}

	return s
}

// Cache returns the local config cache for reading.
func (s *Service) Cache() *Cache {
	return s.cache
}

// StartTombstoneGC launches the background tombstone GC sweep. Idempotent;
// a non-positive tombstoneTTL disables GC (noop). The goroutine lives until
// StopTombstoneGC. Bound to the cell lifecycle via ConfigCore.AfterStart.
func (s *Service) StartTombstoneGC() {
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	if s.gcStarted || s.cache.tombstoneTTL <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.gcCancel = cancel
	s.gcDone = make(chan struct{})
	s.gcStarted = true
	go s.runTombstoneGC(ctx)
}

// StopTombstoneGC signals the GC goroutine and waits for it to drain,
// honoring ctx for the shutdown deadline. Idempotent; safe if never started.
func (s *Service) StopTombstoneGC(ctx context.Context) error {
	s.gcMu.Lock()
	if !s.gcStarted {
		s.gcMu.Unlock()
		return nil
	}
	s.gcCancel()
	done := s.gcDone
	s.gcStarted = false
	s.gcMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runTombstoneGC is the background GC goroutine body. It sweeps tombstones
// on every ticker tick until ctx is canceled.
func (s *Service) runTombstoneGC(ctx context.Context) {
	defer close(s.gcDone)

	ttl := s.cache.tombstoneTTL
	interval := ttl / 2
	if interval <= 0 {
		interval = ttl
	}

	ticker := s.clk.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			s.cache.sweepTombstones(s.clk.Now())
		}
	}
}

func (s *Service) recordConfigEventProcess(ctx context.Context, reason obmetrics.ConfigEventProcessReason) {
	if s.configEventCollector == nil {
		return
	}
	obmetrics.RecordConfigEventProcess(ctx, s.configEventCollector, reason)
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
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleEntryUpserted(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	event, err := configevents.DecodeEntryUpserted(entry.Payload)
	if err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-upserted payload: %w", err)))
	}

	s.cache.mu.Lock()
	known := s.cache.entries[event.Key]
	if event.Version <= known.version {
		s.cache.mu.Unlock()
		s.logger.Debug("config-subscribe: stale or replayed entry-upserted ignored",
			slog.String("key", event.Key),
			slog.Int("incoming_version", event.Version),
			slog.Int("known_version", known.version))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonStale)
		return outbox.Ack()
	}
	s.cache.entries[event.Key] = cacheEntry{version: event.Version, present: true}
	s.cache.mu.Unlock()
	s.logger.Debug("config-subscribe: cache updated",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return outbox.Ack()
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
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleEntryDeleted(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	event, err := configevents.DecodeEntryDeleted(entry.Payload)
	if err != nil {
		s.logger.Error("config-subscribe: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("config-subscribe: unmarshal entry-deleted payload: %w", err)))
	}

	s.cache.mu.Lock()
	known, exists := s.cache.entries[event.Key]
	// Stale-delete guard: drop if the delete predates known state, or if it is
	// replaying the same tombstone that was already accepted.
	// A same-version delete is still accepted when the known entry is present:
	// it is the delete of that currently known entry.
	if event.Version < known.version || (exists && event.Version == known.version && !known.present) {
		s.cache.mu.Unlock()
		s.logger.Debug("config-subscribe: stale entry-deleted ignored",
			slog.String("key", event.Key),
			slog.Int("incoming_version", event.Version),
			slog.Int("known_version", known.version))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonStale)
		return outbox.Ack()
	}
	s.cache.entries[event.Key] = cacheEntry{
		version:   event.Version,
		present:   false,
		deletedAt: s.cache.clk.Now(),
	}
	s.cache.mu.Unlock()
	s.logger.Debug("config-subscribe: key tombstoned in cache",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return outbox.Ack()
}
