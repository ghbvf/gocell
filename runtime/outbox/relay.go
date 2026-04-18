package outbox

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

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

	// Failure budgets for each background loop. nil means disabled (threshold=0).
	// ref: K8s workqueue ItemExponentialFailureRateLimiter — absolute count + Forget.
	pollBudget    *FailureBudget
	reclaimBudget *FailureBudget
	cleanupBudget *FailureBudget
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

	r := &Relay{
		store:   store,
		pub:     pub,
		cfg:     cfg,
		metrics: metrics,
	}
	// Instantiate failure budgets. threshold=0 → nil (disabled).
	if cfg.PollFailureBudget > 0 {
		r.pollBudget = NewFailureBudget("outbox-relay-poll", cfg.PollFailureBudget)
	}
	if cfg.ReclaimFailureBudget > 0 {
		r.reclaimBudget = NewFailureBudget("outbox-relay-reclaim", cfg.ReclaimFailureBudget)
	}
	if cfg.CleanupFailureBudget > 0 {
		r.cleanupBudget = NewFailureBudget("outbox-relay-cleanup", cfg.CleanupFailureBudget)
	}
	return r
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
		r.readyCh = nil // clear so Ready() returns nil between Stop and next Start
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
			err := r.pollOnce(ctx)
			if err != nil {
				slog.Error("outbox relay: poll failed",
					slog.Any("error", err),
				)
			}
			if r.pollBudget != nil {
				r.pollBudget.Record(err)
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
			err := r.reclaimStale(ctx)
			if err != nil {
				slog.Error("outbox relay: reclaim failed",
					slog.Any("error", err),
				)
			}
			if r.reclaimBudget != nil {
				r.reclaimBudget.Record(err)
			}
		}
	}
}

// cleanupLoop runs cleanup data-driven: after each pass it asks the store for
// the oldest published / dead row and sleeps exactly until that row crosses its
// retention window, instead of polling on a fixed timer. Bounded by [floor,
// ceiling] for safety: floor prevents tight-loop on clock skew; ceiling acts as
// a periodic re-check in case OldestEligibleAt itself returns stale info or no
// rows exist for a long time.
func (r *Relay) cleanupLoop(ctx context.Context) {
	for {
		err := r.cleanup(ctx)
		if err != nil {
			slog.Error("outbox relay: cleanup failed", slog.Any("error", err))
		}
		if r.cleanupBudget != nil {
			r.cleanupBudget.Record(err)
		}

		wait := r.nextCleanupWait(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// defaultCleanupWaitFloor and cleanupWaitCeiling bound the cleanup wake-up sleep.
// Floor avoids tight-loop on clock skew. Ceiling forces a periodic re-check
// even when the table is empty (OldestEligibleAt returns ok=false).
const (
	defaultCleanupWaitFloor = 5 * time.Second
	cleanupWaitCeiling      = 1 * time.Hour
)

// nextCleanupWait computes how long the cleanup loop should sleep before the
// next pass: min(time-until-next-published-eligible, time-until-next-dead-eligible),
// clamped to [floor, ceiling]. Returns ceiling when the store reports no
// candidates for either status (idle table) or any error (defensive: keep
// the loop alive but back off).
func (r *Relay) nextCleanupWait(ctx context.Context) time.Duration {
	now := time.Now()
	wait := cleanupWaitCeiling

	if pubAt, ok := r.oldestOrZero(ctx, "published"); ok {
		if d := pubAt.Add(r.cfg.RetentionPeriod).Sub(now); d < wait {
			wait = d
		}
	}
	if deadAt, ok := r.oldestOrZero(ctx, "dead"); ok {
		if d := deadAt.Add(r.cfg.DeadRetentionPeriod).Sub(now); d < wait {
			wait = d
		}
	}

	floor := r.cleanupWaitFloor()
	if wait < floor {
		wait = floor
	}
	return wait
}

// cleanupWaitFloor returns the effective cleanup floor duration:
// cfg.CleanupWaitFloor if positive, otherwise defaultCleanupWaitFloor.
func (r *Relay) cleanupWaitFloor() time.Duration {
	if r.cfg.CleanupWaitFloor > 0 {
		return r.cfg.CleanupWaitFloor
	}
	return defaultCleanupWaitFloor
}

// oldestOrZero wraps Store.OldestEligibleAt with logging and an idle-fallback:
// any error degrades to ok=false so nextCleanupWait falls back to the ceiling
// instead of tight-looping.
func (r *Relay) oldestOrZero(ctx context.Context, status string) (time.Time, bool) {
	at, ok, err := r.store.OldestEligibleAt(ctx, status)
	if err != nil {
		slog.Warn("outbox relay: OldestEligibleAt failed, backing off to ceiling",
			slog.String("status", status),
			slog.Any("error", err),
		)
		return time.Time{}, false
	}
	return at, ok
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
	for i, res := range results {
		if res.err == nil {
			updated, err := r.store.MarkPublished(ctx, res.entry.ID)
			if err != nil {
				remaining := len(results) - i
				slog.Error("outbox relay: writeBack failed mid-batch, remaining entries stay in claiming",
					slog.Int("completed", i),
					slog.Int("remaining", remaining),
					slog.Any("error", err))
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
				remaining := len(results) - i
				slog.Error("outbox relay: writeBack failed mid-batch, remaining entries stay in claiming",
					slog.Int("completed", i),
					slog.Int("remaining", remaining),
					slog.Any("error", err))
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
	errMsg := SanitizeError(res.err.Error(), 1000)

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

// ---------------------------------------------------------------------------
// Health and readiness
// ---------------------------------------------------------------------------

// HealthCheckers returns a map of named health checker functions, one per
// enabled failure budget. The returned functions implement the
// health.Checker contract: nil return = healthy; non-nil = unhealthy.
//
// Only budgets with a positive threshold are included; threshold=0 (disabled)
// budgets are excluded from the map so callers can safely iterate all entries
// and register them unconditionally.
//
// ref: controller-runtime/pkg/healthz AddReadyzCheck — named-checker aggregation.
func (r *Relay) HealthCheckers() map[string]func() error {
	m := make(map[string]func() error)
	if r.pollBudget != nil {
		m["outbox-relay-poll"] = r.pollBudget.Checker()
	}
	if r.reclaimBudget != nil {
		m["outbox-relay-reclaim"] = r.reclaimBudget.Checker()
	}
	if r.cleanupBudget != nil {
		m["outbox-relay-cleanup"] = r.cleanupBudget.Checker()
	}
	return m
}

// Ready returns the channel that is closed when Start() has transitioned the
// relay to the running state. Callers can use this to synchronise without
// polling:
//
//	select {
//	case <-relay.Ready():
//	    // relay is running
//	case <-time.After(deadline):
//	    // timeout
//	}
//
// If Start() has not been called yet, Ready() returns nil — a nil channel
// blocks forever, which callers should handle with a timeout.
func (r *Relay) Ready() <-chan struct{} {
	r.mu.Lock()
	ch := r.readyCh
	r.mu.Unlock()
	return ch
}

// retryDelay computes exponential backoff with jitter and cap.
// Formula: cappedDelay(base * 2^attempts) + jitter([0, delay/4])
// ref: adapters/rabbitmq/consumer_base.go claimWithRetry backoff
func (r *Relay) retryDelay(attempts int) time.Duration {
	// Clamp shift exponent to avoid int64 overflow when attempts is unexpectedly
	// large (defensive: real callers stop at MaxAttempts ≤ 10). 1<<30 * 5s already
	// far exceeds MaxRetryDelay so the cap kicks in identically.
	shift := min(attempts, 30)
	delay := r.cappedDelay(r.cfg.BaseRetryDelay * (1 << shift))
	if delay > 0 {
		jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
		delay += jitter
	}
	return delay
}
