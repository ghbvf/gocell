package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Compile-time interface check.
var _ outbox.Relay = (*OutboxRelay)(nil)

// Note: OutboxRelay also satisfies runtime/worker.Worker via structural typing
// (Start/Stop methods match), but we do not import runtime/worker here to
// maintain the adapters → kernel dependency direction.

// ---------------------------------------------------------------------------
// Outbox entry status constants
// ---------------------------------------------------------------------------

const (
	statusPending   = "pending"   // awaiting publish (including retries)
	statusClaiming  = "claiming"  // locked by a relay instance, publishing in progress
	statusPublished = "published" // successfully delivered to broker
	statusDead      = "dead"      // exceeded MaxAttempts, requires manual intervention
)

// ---------------------------------------------------------------------------
// Relay-internal types (adapter layer, not in kernel/outbox)
// ---------------------------------------------------------------------------

// relayEntry wraps outbox.Entry with relay runtime state.
// Attempts is kept in the adapter layer to avoid polluting the kernel model (F-9).
type relayEntry struct {
	outbox.Entry
	Attempts int
}

// publishResult records the outcome of publishing a single entry.
type publishResult struct {
	entry relayEntry
	err   error
}

// pollStats records per-poll-cycle counters for observability.
type pollStats struct {
	published int
	retried   int
	dead      int
	skipped   int
}

// outboxMessage is the wire envelope sent to the broker.
// Only includes fields consumers need; relay-internal state (Attempts, status)
// is never serialised to the wire.
//
// ref: Watermill message.Message — payload + metadata envelope
type outboxMessage struct {
	ID        string            `json:"id"`
	EventType string            `json:"eventType"`
	Topic     string            `json:"topic,omitempty"`
	Payload   json.RawMessage   `json:"payload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// relayState — lifecycle state machine
// ---------------------------------------------------------------------------

type relayState int32

const (
	relayStopped  relayState = iota // zero value = stopped
	relayStarting                   // Start() entered, goroutines launching
	relayRunning                    // poll/cleanup/reclaim loops active
	relayStopping                   // Stop() called, waiting for goroutines
)

// ---------------------------------------------------------------------------
// RelayConfig
// ---------------------------------------------------------------------------

// RelayConfig configures the outbox relay behaviour.
type RelayConfig struct {
	// PollInterval is how often the relay polls for pending entries.
	PollInterval time.Duration
	// BatchSize is the maximum number of entries fetched per poll cycle.
	BatchSize int
	// RetentionPeriod is how long published entries are kept before cleanup.
	RetentionPeriod time.Duration
	// MaxAttempts is the maximum number of publish attempts before an entry
	// is marked as dead-lettered. Default 5.
	MaxAttempts int
	// BaseRetryDelay is the base delay for exponential backoff. Default 5s.
	// Actual delay = cappedDelay(BaseRetryDelay * 2^attempts) + jitter.
	BaseRetryDelay time.Duration
	// ClaimTTL is how long a claiming entry is held before reclaimStale
	// recovers it back to pending. Default 60s.
	ClaimTTL time.Duration
	// MaxRetryDelay caps the exponential backoff delay. Default 5m. (F-7)
	MaxRetryDelay time.Duration
	// ReclaimInterval is how often reclaimStale runs. Default 30s. (S4-F2)
	ReclaimInterval time.Duration
}

// DefaultRelayConfig returns a RelayConfig with sensible defaults.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		PollInterval:    1 * time.Second,
		BatchSize:       100,
		RetentionPeriod: 72 * time.Hour,
		MaxAttempts:     5,
		BaseRetryDelay:  5 * time.Second,
		ClaimTTL:        60 * time.Second,
		MaxRetryDelay:   5 * time.Minute,
		ReclaimInterval: 30 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// relayDB interface
// ---------------------------------------------------------------------------

// relayDB abstracts the database operations needed by OutboxRelay.
type relayDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ---------------------------------------------------------------------------
// OutboxRelay
// ---------------------------------------------------------------------------

// OutboxRelay polls unpublished outbox entries from PostgreSQL and publishes
// them via the provided outbox.Publisher using a three-phase approach:
//
//	Phase 1 (claim):   short tx — SELECT FOR UPDATE SKIP LOCKED + SET status='claiming'
//	Phase 2 (publish): outside tx — publish each entry to broker
//	Phase 3 (writeBack): short tx — mark published/retry/dead based on outcome
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
// reclaimStale: claiming entries past ClaimTTL are recovered with attempts++.
// If attempts reaches MaxAttempts during reclaim, the entry is marked dead.
type OutboxRelay struct {
	db     relayDB
	pub    outbox.Publisher
	config RelayConfig

	// state is the lifecycle state machine (atomic for lock-free reads).
	state atomic.Int32

	// mu protects lifecycle state shared by Start and Stop.
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{} // closed once Start() transitions to relayRunning
	wg      sync.WaitGroup

	// writeBackHook is called between publishAll and writeBack, for testing only.
	// When non-nil, pollOnce calls it after Phase 2 before Phase 3.
	writeBackHook func()
}

// NewOutboxRelay creates an OutboxRelay that polls from db and publishes via pub.
// db is typically pool.DB() (*pgxpool.Pool satisfies relayDB).
// Zero or negative config values are replaced with defaults to prevent panics
// (e.g. time.NewTicker(0) panics).
func NewOutboxRelay(db relayDB, pub outbox.Publisher, cfg RelayConfig) *OutboxRelay {
	defaults := DefaultRelayConfig()
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaults.PollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaults.BatchSize
	}
	if cfg.RetentionPeriod <= 0 {
		cfg.RetentionPeriod = defaults.RetentionPeriod
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaults.MaxAttempts
	}
	if cfg.BaseRetryDelay <= 0 {
		cfg.BaseRetryDelay = defaults.BaseRetryDelay
	}
	if cfg.ClaimTTL <= 0 {
		cfg.ClaimTTL = defaults.ClaimTTL
	}
	if cfg.MaxRetryDelay <= 0 {
		cfg.MaxRetryDelay = defaults.MaxRetryDelay
	}
	if cfg.ReclaimInterval <= 0 {
		cfg.ReclaimInterval = defaults.ReclaimInterval
	}

	// Guard: ClaimTTL must exceed 2x PollInterval to prevent reclaimStale
	// from reclaiming entries still being processed (S2-F2).
	if cfg.ClaimTTL <= cfg.PollInterval*2 {
		slog.Warn("outbox relay: ClaimTTL should be > 2*PollInterval to avoid premature reclaim",
			slog.Duration("claim_ttl", cfg.ClaimTTL),
			slog.Duration("poll_interval", cfg.PollInterval))
	}

	return &OutboxRelay{
		db:     db,
		pub:    pub,
		config: cfg,
	}
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// cappedDelay caps a duration at MaxRetryDelay, matching ConsumerBase's pattern.
// ref: adapters/rabbitmq/consumer_base.go cappedDelay
func (r *OutboxRelay) cappedDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > r.config.MaxRetryDelay {
		return r.config.MaxRetryDelay
	}
	return d
}

// retryDelay computes exponential backoff with jitter and cap.
// Formula: cappedDelay(base * 2^attempts) + jitter([0, delay/4])
// ref: adapters/rabbitmq/consumer_base.go claimWithRetry backoff
func (r *OutboxRelay) retryDelay(attempts int) time.Duration {
	delay := r.cappedDelay(r.config.BaseRetryDelay * (1 << attempts))
	if delay > 0 {
		jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
		delay += jitter
	}
	return delay
}

// truncateError truncates an error message to maxLen bytes.
func truncateError(msg string, maxLen int) string {
	if len(msg) > maxLen {
		return msg[:maxLen]
	}
	return msg
}


// Start begins the relay polling loop, cleanup goroutine, and reclaim loop.
// It blocks until ctx is cancelled or Stop is called.
func (r *OutboxRelay) Start(ctx context.Context) error {
	if !r.state.CompareAndSwap(int32(relayStopped), int32(relayStarting)) {
		return errcode.New(ErrAdapterPGConnect, "outbox relay already started")
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
		slog.Duration("poll_interval", r.config.PollInterval),
		slog.Int("batch_size", r.config.BatchSize),
		slog.Int("max_attempts", r.config.MaxAttempts),
		slog.Duration("claim_ttl", r.config.ClaimTTL),
	)

	<-ctx.Done()
	return nil
}

// Stop signals the relay to shut down gracefully and waits for goroutines.
// It respects the caller's context deadline: if ctx expires before goroutines
// finish, Stop returns an error instead of blocking indefinitely.
func (r *OutboxRelay) Stop(ctx context.Context) error {
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
		return errcode.Wrap(ErrAdapterPGConnect, "relay stop: timed out waiting for start", ctx.Err())
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
		return errcode.Wrap(ErrAdapterPGConnect, "relay stop timeout", ctx.Err())
	}
}

// pollLoop fetches unpublished entries and publishes them.
func (r *OutboxRelay) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(r.config.PollInterval)
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

// pollOnce executes the three-phase relay cycle: claim → publish → writeBack.
func (r *OutboxRelay) pollOnce(ctx context.Context) error {
	start := time.Now()

	// Phase 1: Claim (short tx)
	entries, err := r.claim(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	claimDur := time.Since(start)

	// Test hook: allows injecting reclaimStale between Phase 2 and Phase 3
	// to verify optimistic lock behavior (F-8).
	if r.writeBackHook != nil {
		r.writeBackHook()
	}

	// Phase 2: Publish (outside tx)
	pubStart := time.Now()
	results := r.publishAll(ctx, entries)
	pubDur := time.Since(pubStart)

	// Phase 3: WriteBack (short tx)
	stats, wbErr := r.writeBack(ctx, results)

	slog.Info("outbox relay: poll complete",
		slog.Int("published", stats.published),
		slog.Int("retried", stats.retried),
		slog.Int("dead_lettered", stats.dead),
		slog.Int("skipped", stats.skipped),
		slog.Duration("claim_dur", claimDur),
		slog.Duration("publish_dur", pubDur),
	)

	return wbErr
}

// claim locks a batch of pending entries in a short transaction.
func (r *OutboxRelay) claim(ctx context.Context) ([]relayEntry, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "outbox relay: claim begin tx", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	// ORDER BY matches idx_outbox_pending (next_retry_at NULLS FIRST, created_at) (F-3).
	const claimQuery = `UPDATE outbox_entries
		SET status = $1, claimed_at = now()
		WHERE id IN (
			SELECT id FROM outbox_entries
			WHERE status = $2
				AND (next_retry_at IS NULL OR next_retry_at <= now())
			ORDER BY next_retry_at NULLS FIRST, created_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, aggregate_id, aggregate_type, event_type,
			topic, payload, metadata, created_at, attempts`

	rows, err := tx.Query(ctx, claimQuery, statusClaiming, statusPending, r.config.BatchSize)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: claim query failed", err)
	}
	defer rows.Close()

	var entries []relayEntry
	for rows.Next() {
		var (
			e            relayEntry
			metadataJSON []byte
		)
		if scanErr := rows.Scan(
			&e.ID, &e.AggregateID, &e.AggregateType, &e.EventType,
			&e.Topic, &e.Payload, &metadataJSON, &e.CreatedAt, &e.Attempts,
		); scanErr != nil {
			return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: claim scan failed", scanErr)
		}
		if len(metadataJSON) > 0 {
			if jsonErr := json.Unmarshal(metadataJSON, &e.Metadata); jsonErr != nil {
				slog.Warn("outbox relay: metadata unmarshal failed",
					slog.String("entry_id", e.ID),
					slog.Any("error", jsonErr),
				)
			}
		}
		entries = append(entries, e)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: claim rows iteration failed", rows.Err())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "outbox relay: claim commit failed", err)
	}
	committed = true

	return entries, nil
}

// publishAll publishes each entry to the broker outside of any transaction.
// Uses outboxMessage wire envelope (S1-F1) to avoid leaking internal fields.
func (r *OutboxRelay) publishAll(ctx context.Context, entries []relayEntry) []publishResult {
	results := make([]publishResult, len(entries))
	for i, e := range entries {
		msg := outboxMessage{
			ID:        e.ID,
			EventType: e.EventType,
			Topic:     e.RoutingTopic(),
			Payload:   json.RawMessage(e.Payload),
			Metadata:  e.Metadata,
			CreatedAt: e.CreatedAt,
		}
		payload, marshalErr := json.Marshal(msg)
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

// writeBack updates entry statuses based on publish outcomes in a short transaction.
// All UPDATEs include WHERE status='claiming' as an optimistic lock (F-8).
func (r *OutboxRelay) writeBack(ctx context.Context, results []publishResult) (pollStats, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return pollStats{}, errcode.Wrap(ErrAdapterPGConnect, "outbox relay: writeBack begin tx", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	var stats pollStats

	for _, res := range results {
		if res.err == nil {
			// Success → mark published (optimistic lock on status).
			ct, execErr := tx.Exec(ctx,
				`UPDATE outbox_entries SET status = $1, published_at = now()
				 WHERE id = $2 AND status = $3`,
				statusPublished, res.entry.ID, statusClaiming)
			if execErr != nil {
				return stats, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: writeBack mark published", execErr)
			}
			if ct.RowsAffected() == 0 {
				// Entry was reclaimed by reclaimStale — skip (at-least-once OK).
				stats.skipped++
				continue
			}
			stats.published++
		} else {
			newAttempts := res.entry.Attempts + 1
			errMsg := truncateError(res.err.Error(), 1000)

			if newAttempts >= r.config.MaxAttempts {
				// Dead-letter: exceeded max attempts.
				if _, execErr := tx.Exec(ctx,
					`UPDATE outbox_entries SET status = $1, attempts = $2, last_error = $3
					 WHERE id = $4 AND status = $5`,
					statusDead, newAttempts, errMsg, res.entry.ID, statusClaiming); execErr != nil {
					return stats, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: writeBack mark dead", execErr)
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
				// Retry: back to pending with exponential backoff + jitter (F-6).
				delay := r.retryDelay(newAttempts)
				if _, execErr := tx.Exec(ctx,
					`UPDATE outbox_entries SET status = $1, attempts = $2,
					 next_retry_at = now() + $3::interval, last_error = $4
					 WHERE id = $5 AND status = $6`,
					statusPending, newAttempts, delay, errMsg, res.entry.ID, statusClaiming); execErr != nil {
					return stats, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: writeBack mark retry", execErr)
				}
				stats.retried++
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, errcode.Wrap(ErrAdapterPGConnect, "outbox relay: writeBack commit", err)
	}
	committed = true

	return stats, nil
}

// reclaimStale recovers entries stuck in 'claiming' past ClaimTTL.
// It increments attempts and marks dead if MaxAttempts is reached (F-4).
func (r *OutboxRelay) reclaimStale(ctx context.Context) error {
	const q = `UPDATE outbox_entries
		SET status = CASE WHEN attempts + 1 >= $2 THEN $3 ELSE $4 END,
			attempts = attempts + 1,
			claimed_at = NULL,
			next_retry_at = CASE WHEN attempts + 1 >= $2 THEN NULL
				ELSE now() + ($5 * power(2, attempts + 1))::interval END
		WHERE status = $6 AND claimed_at < now() - $1::interval`

	ct, err := r.db.Exec(ctx, q,
		r.config.ClaimTTL, r.config.MaxAttempts,
		statusDead, statusPending,
		r.config.BaseRetryDelay, statusClaiming)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: reclaimStale failed", err)
	}
	if ct.RowsAffected() > 0 {
		slog.Warn("outbox relay: reclaimed stale entries",
			slog.Int64("count", ct.RowsAffected()),
		)
	}
	return nil
}

// reclaimLoop periodically runs reclaimStale at ReclaimInterval.
func (r *OutboxRelay) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(r.config.ReclaimInterval)
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
func (r *OutboxRelay) cleanupLoop(ctx context.Context) {
	// Run cleanup at 10x the poll interval (minimum 10s).
	interval := r.config.PollInterval * 10
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-r.config.RetentionPeriod)
			if err := r.deletePublishedBefore(ctx, cutoff); err != nil {
				slog.Error("outbox relay: cleanup failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// deletePublishedBefore removes published entries older than the cutoff time.
// Also cleans up dead entries past the same retention period (S2-F1).
func (r *OutboxRelay) deletePublishedBefore(ctx context.Context, before time.Time) error {
	const publishedQuery = `DELETE FROM outbox_entries WHERE status = $1 AND published_at < $2`
	ct, err := r.db.Exec(ctx, publishedQuery, statusPublished, before)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: delete published entries failed", err)
	}
	if ct.RowsAffected() > 0 {
		slog.Info("outbox relay: cleaned up published entries",
			slog.Int64("deleted", ct.RowsAffected()),
		)
	}

	// Clean up dead entries past retention (uses created_at as dead entries have no published_at).
	const deadQuery = `DELETE FROM outbox_entries WHERE status = $1 AND created_at < $2`
	ct, err = r.db.Exec(ctx, deadQuery, statusDead, before)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: delete dead entries failed", err)
	}
	if ct.RowsAffected() > 0 {
		slog.Info("outbox relay: cleaned up dead entries",
			slog.Int64("deleted", ct.RowsAffected()),
		)
	}

	return nil
}
