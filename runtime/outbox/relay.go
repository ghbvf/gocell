package outbox

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Compile-time interface checks.
var _ kout.Relay = (*Relay)(nil)
var _ worker.Worker = (*Relay)(nil)

// ---------------------------------------------------------------------------
// Relay lifecycle state machine
// ---------------------------------------------------------------------------

type relayState int32

const (
	relayStopped  relayState = iota // zero value = stopped
	relayStarting                   // Start() entered, goroutines launching
	relayRunning                    // poll/cleanup/reclaim loops active
	relayStopping                   // Stop() called, waiting for goroutines
)

// ---------------------------------------------------------------------------
// Internal relay error codes
// ---------------------------------------------------------------------------

const (
	// errRelayOp is used for lifecycle (start/stop) errors.
	errRelayOp errcode.Code = "ERR_OUTBOX_RELAY_OP"
)

// ---------------------------------------------------------------------------
// publishResult records the outcome of publishing a single entry.
// ---------------------------------------------------------------------------

type publishResult struct {
	entry ClaimedEntry
	err   error
}

// pollStats records per-poll-cycle counters for observability.
type pollStats struct {
	published int
	retried   int
	dead      int
	skipped   int
}

// ---------------------------------------------------------------------------
// Relay
// ---------------------------------------------------------------------------

// Relay polls unpublished outbox entries via a Store interface and publishes
// them via the provided outbox.Publisher using a three-phase approach:
//
//	Phase 1 (claim):     Store.ClaimPending — short tx in the Store impl
//	Phase 2 (publish):   outside tx — publish each entry to broker
//	Phase 3 (writeBack): Store.MarkPublished / MarkRetry / MarkDead — short tx each
//
// Consistency level: L2 (OutboxFact)
//
// Outbox entry state machine:
//
//	pending ──claim──→ claiming ──publish ok──→ published ──retention──→ (deleted)
//	   ↑                  │
//	   │ (fail, attempts < max)
//	   └──────────────────┘
//	                      │ (fail, attempts >= max)
//	                      ↓
//	                     dead ──dead retention──→ (deleted)
//
// ReclaimStale: claiming entries past ClaimTTL are recovered with attempts++.
// If attempts reaches MaxAttempts during reclaim, the entry is marked dead.
//
// ref: Watermill router.go — goroutine-per-handler lifecycle pattern
type Relay struct {
	store   Store
	pub     kout.Publisher
	cfg     RelayConfig
	metrics kout.RelayCollector

	// state is the lifecycle state machine (atomic for lock-free reads).
	state atomic.Int32

	// mu protects lifecycle state shared by Start and Stop.
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{} // closed once Start() transitions to relayRunning

	wg sync.WaitGroup
}

// NewRelay creates a Relay that polls from store and publishes via pub.
// Zero or negative cfg values are replaced with defaults via cfg.WithDefaults().
// A nil Metrics is replaced with NoopRelayCollector; the collector is then
// wrapped in safeRelayCollector so panics cannot crash relay goroutines.
func NewRelay(store Store, pub kout.Publisher, cfg RelayConfig) *Relay {
	cfg = cfg.WithDefaults()
	if cfg.Metrics == nil {
		cfg.Metrics = kout.NoopRelayCollector{}
	}
	// Wrap in safe adapter: collector panics must not crash relay goroutines.
	// ref: runtime/http/middleware/safe_observe.go — same pattern for HTTP metrics.
	metrics := &safeRelayCollector{inner: cfg.Metrics}

	// Guard: ClaimTTL must exceed 2x PollInterval to prevent ReclaimStale
	// from reclaiming entries still being processed.
	if cfg.ClaimTTL <= cfg.PollInterval*2 {
		slog.Warn("outbox relay: ClaimTTL should be > 2*PollInterval to avoid premature reclaim",
			slog.Duration("claim_ttl", cfg.ClaimTTL),
			slog.Duration("poll_interval", cfg.PollInterval))
	}

	return &Relay{
		store:   store,
		pub:     pub,
		cfg:     cfg,
		metrics: metrics,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins the relay polling loop, cleanup goroutine, and reclaim loop.
// It blocks until ctx is cancelled or Stop is called.
func (r *Relay) Start(ctx context.Context) error {
	if !r.state.CompareAndSwap(int32(relayStopped), int32(relayStarting)) {
		return errcode.New(errRelayOp, "outbox relay already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ready := make(chan struct{})

	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.readyCh = ready
	r.wg.Add(3)
	r.mu.Unlock()

	r.state.Store(int32(relayRunning))
	close(ready)

	defer func() {
		r.wg.Wait()

		r.mu.Lock()
		r.cancel = nil
		r.done = nil
		r.state.Store(int32(relayStopped))
		close(done)
		r.mu.Unlock()
	}()

	go func() {
		defer r.wg.Done()
		r.pollLoop(ctx)
	}()

	go func() {
		defer r.wg.Done()
		r.cleanupLoop(ctx)
	}()

	go func() {
		defer r.wg.Done()
		r.reclaimLoop(ctx)
	}()

	slog.Info("outbox relay: started",
		slog.Duration("poll_interval", r.cfg.PollInterval),
		slog.Int("batch_size", r.cfg.BatchSize),
		slog.Int("max_attempts", r.cfg.MaxAttempts),
		slog.Duration("claim_ttl", r.cfg.ClaimTTL),
	)

	<-ctx.Done()
	return nil
}

// Stop signals the relay to shut down gracefully and waits for goroutines.
// It respects the caller's context deadline: if ctx expires before goroutines
// finish, Stop returns an error instead of blocking indefinitely.
func (r *Relay) Stop(ctx context.Context) error {
	// If never started, no-op (consistent with worker.Worker contract).
	r.mu.Lock()
	ready := r.readyCh
	r.mu.Unlock()

	if ready == nil {
		return nil
	}

	// Wait for Start() to transition to relayRunning.
	select {
	case <-ready:
	case <-ctx.Done():
		return errcode.Wrap(errRelayOp, "relay stop: timed out waiting for start", ctx.Err())
	}

	r.state.Store(int32(relayStopping))

	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}

	select {
	case <-done:
		slog.Info("outbox relay: stopped")
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errRelayOp, "relay stop timeout", ctx.Err())
	}
}

// ---------------------------------------------------------------------------
// Background loops
// ---------------------------------------------------------------------------

// pollLoop fetches unpublished entries and publishes them.
func (r *Relay) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.pollOnce(ctx); err != nil {
				slog.Error("outbox relay: poll failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// reclaimLoop periodically runs reclaimStale at ReclaimInterval.
func (r *Relay) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.ReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reclaimStale(ctx); err != nil {
				slog.Error("outbox relay: reclaim failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// cleanupLoop periodically deletes old published entries.
func (r *Relay) cleanupLoop(ctx context.Context) {
	// Run cleanup at 10x the poll interval (minimum 10s).
	interval := max(r.cfg.PollInterval*10, 10*time.Second)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.cleanup(ctx); err != nil {
				slog.Error("outbox relay: cleanup failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// pollOnce — three-phase relay cycle
// ---------------------------------------------------------------------------

// pollOnce executes the three-phase relay cycle: claim → publish → writeBack.
func (r *Relay) pollOnce(ctx context.Context) error {
	start := time.Now()

	// Phase 1: Claim
	entries, err := r.store.ClaimPending(ctx, r.cfg.BatchSize)
	if err != nil {
		return err
	}

	// Record batch size even for empty batches (captures idle cycles).
	r.metrics.RecordBatchSize(len(entries))

	if len(entries) == 0 {
		return nil
	}
	claimDur := time.Since(start)

	// Phase 2: Publish (outside tx)
	pubStart := time.Now()
	results := r.publishBatch(ctx, entries)
	pubDur := time.Since(pubStart)

	// Phase 3: WriteBack
	wbStart := time.Now()
	stats, wbErr := r.writeBackResults(ctx, results)
	wbDur := time.Since(wbStart)

	// Log and record metrics only after writeBack completes — if commit
	// fails, stats are rolled back and recording them would be misleading.
	if wbErr == nil {
		slog.Info("outbox relay: poll complete",
			slog.Int("published", stats.published),
			slog.Int("retried", stats.retried),
			slog.Int("dead_lettered", stats.dead),
			slog.Int("skipped", stats.skipped),
			slog.Duration("claim_dur", claimDur),
			slog.Duration("publish_dur", pubDur),
		)
		r.metrics.RecordPollCycle(kout.PollCycleResult{
			Published:    stats.published,
			Retried:      stats.retried,
			Dead:         stats.dead,
			Skipped:      stats.skipped,
			ClaimDur:     claimDur,
			PublishDur:   pubDur,
			WriteBackDur: wbDur,
		})
	}

	return wbErr
}

// publishBatch publishes each entry to the broker outside of any transaction.
// Uses MarshalEnvelope to produce the wire envelope with camelCase JSON keys.
// ref: Watermill router.go publishBatch — per-message outcome, no batch atomicity
func (r *Relay) publishBatch(ctx context.Context, entries []ClaimedEntry) []publishResult {
	results := make([]publishResult, len(entries))
	for i, e := range entries {
		payload, marshalErr := MarshalEnvelope(e)
		if marshalErr != nil {
			results[i] = publishResult{entry: e, err: marshalErr}
			continue
		}
		results[i] = publishResult{
			entry: e,
			err:   r.pub.Publish(ctx, e.RoutingTopic(), payload),
		}
	}
	return results
}

// writeBackResults updates entry statuses based on publish outcomes.
// Each Store method call uses its own short transaction with an optimistic
// lock on status='claiming' — this prevents a race where ReclaimStale
// recovers the entry between Phase 2 and Phase 3.
func (r *Relay) writeBackResults(ctx context.Context, results []publishResult) (pollStats, error) {
	var stats pollStats
	for _, res := range results {
		if res.err == nil {
			updated, err := r.store.MarkPublished(ctx, res.entry.ID)
			if err != nil {
				return stats, err
			}
			if !updated {
				// Entry was reclaimed by ReclaimStale — skip (at-least-once OK).
				stats.skipped++
			} else {
				stats.published++
			}
		} else {
			if err := r.handleFailedEntry(ctx, res, &stats); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

// handleFailedEntry handles a single failed publish result, updating stats.
// Extracted to keep writeBackResults below cognitive-complexity ceiling.
func (r *Relay) handleFailedEntry(ctx context.Context, res publishResult, stats *pollStats) error {
	newAttempts := res.entry.Attempts + 1
	errMsg := sanitizeError(res.err.Error(), 1000)

	if newAttempts >= r.cfg.MaxAttempts {
		_, err := r.store.MarkDead(ctx, res.entry.ID, newAttempts, errMsg)
		if err != nil {
			return err
		}
		stats.dead++
		slog.Error("outbox relay: entry dead-lettered",
			slog.String("entry_id", res.entry.ID),
			slog.String("event_type", res.entry.EventType),
			slog.String("aggregate_id", res.entry.AggregateID),
			slog.Int("attempts", newAttempts),
			slog.String("last_error", errMsg),
		)
	} else {
		// Retry: back to pending with exponential backoff + jitter,
		// preventing thundering herd in multi-relay-instance deployments.
		delay := r.retryDelay(newAttempts)
		nextRetryAt := time.Now().Add(delay)
		_, err := r.store.MarkRetry(ctx, res.entry.ID, newAttempts, nextRetryAt, errMsg)
		if err != nil {
			return err
		}
		stats.retried++
	}
	return nil
}

// ---------------------------------------------------------------------------
// reclaimStale / cleanup helpers
// ---------------------------------------------------------------------------

// reclaimStale recovers entries stuck in 'claiming' past ClaimTTL.
func (r *Relay) reclaimStale(ctx context.Context) error {
	count, err := r.store.ReclaimStale(ctx, r.cfg.ClaimTTL, r.cfg.MaxAttempts, r.cfg.BaseRetryDelay, r.cfg.MaxRetryDelay)
	if err != nil {
		return err
	}
	if count > 0 {
		slog.Warn("outbox relay: reclaimed stale entries",
			slog.Int("count", count),
		)
		r.metrics.RecordReclaim(int64(count))
	}
	return nil
}

// cleanup removes old published and dead entries.
// Loops CleanupPublished / CleanupDead until each returns deleted < batchSize.
//
// NOTE: If an intermediate batch call fails, this function returns early and
// RecordCleanup is not called — already-deleted rows are not counted.
// This is intentionally conservative: under-counting cleanup is safer than
// over-counting.
func (r *Relay) cleanup(ctx context.Context) error {
	const batchLimit = 1000

	publishedCutoff := time.Now().Add(-r.cfg.RetentionPeriod)
	var totalPublished int64
	for {
		deleted, err := r.store.CleanupPublished(ctx, publishedCutoff, batchLimit)
		if err != nil {
			return err
		}
		totalPublished += int64(deleted)
		if deleted < batchLimit {
			break
		}
	}
	if totalPublished > 0 {
		slog.Info("outbox relay: cleaned up published entries",
			slog.Int64("deleted", totalPublished),
		)
	}

	deadCutoff := time.Now().Add(-r.cfg.DeadRetentionPeriod)
	var totalDead int64
	for {
		deleted, err := r.store.CleanupDead(ctx, deadCutoff, batchLimit)
		if err != nil {
			return err
		}
		totalDead += int64(deleted)
		if deleted < batchLimit {
			break
		}
	}
	if totalDead > 0 {
		slog.Info("outbox relay: cleaned up dead entries",
			slog.Int64("deleted", totalDead),
		)
	}

	if totalPublished > 0 || totalDead > 0 {
		r.metrics.RecordCleanup(totalPublished, totalDead)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backoff helpers
// ---------------------------------------------------------------------------

// cappedDelay caps a duration at MaxRetryDelay.
// ref: adapters/rabbitmq/consumer_base.go cappedDelay
func (r *Relay) cappedDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > r.cfg.MaxRetryDelay {
		return r.cfg.MaxRetryDelay
	}
	return d
}

// retryDelay computes exponential backoff with jitter and cap.
// Formula: cappedDelay(base * 2^attempts) + jitter([0, delay/4])
// ref: adapters/rabbitmq/consumer_base.go claimWithRetry backoff
func (r *Relay) retryDelay(attempts int) time.Duration {
	delay := r.cappedDelay(r.cfg.BaseRetryDelay * (1 << attempts))
	if delay > 0 {
		jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
		delay += jitter
	}
	return delay
}

// ---------------------------------------------------------------------------
// Error sanitization helpers
// ---------------------------------------------------------------------------

// truncateError truncates an error message to maxLen runes, preserving valid
// UTF-8 (avoids splitting multi-byte characters at byte boundaries).
func truncateError(msg string, maxLen int) string {
	if utf8.RuneCountInString(msg) <= maxLen {
		return msg
	}
	runes := []rune(msg)
	return string(runes[:maxLen])
}

// sensitivePatterns matches common sensitive substrings in error messages
// (connection strings, hostnames, credentials) to redact before storage.
var sensitivePatterns = regexp.MustCompile(
	`(?i)(password|passwd|secret|token|dsn|connection[_ ]?string)=[^\s;,]+`,
)

// sanitizeError truncates and redacts sensitive patterns from an error message
// before storing it in the last_error column.
func sanitizeError(errMsg string, maxLen int) string {
	redacted := sensitivePatterns.ReplaceAllString(errMsg, "$1=<REDACTED>")
	return truncateError(redacted, maxLen)
}
