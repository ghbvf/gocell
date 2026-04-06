package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Compile-time interface checks.
var (
	_ outbox.Relay  = (*OutboxRelay)(nil)
	_ worker.Worker = (*OutboxRelay)(nil)
)

// RelayConfig configures the outbox relay behaviour.
type RelayConfig struct {
	// PollInterval is how often the relay polls for unpublished entries.
	PollInterval time.Duration
	// BatchSize is the maximum number of entries fetched per poll cycle.
	BatchSize int
	// RetentionPeriod is how long published entries are kept before cleanup.
	RetentionPeriod time.Duration
}

// DefaultRelayConfig returns a RelayConfig with sensible defaults.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		PollInterval:    1 * time.Second,
		BatchSize:       100,
		RetentionPeriod: 72 * time.Hour,
	}
}

// relayDB abstracts the database operations needed by OutboxRelay.
type relayDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// OutboxRelay polls unpublished outbox entries from PostgreSQL and publishes
// them via the provided outbox.Publisher. Entries are marked as published only
// after successful delivery.
type OutboxRelay struct {
	db     relayDB
	pub    outbox.Publisher
	config RelayConfig

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
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
	return &OutboxRelay{
		db:     db,
		pub:    pub,
		config: cfg,
	}
}

// Start begins the relay polling loop and cleanup goroutine. It blocks until
// ctx is cancelled or Stop is called.
func (r *OutboxRelay) Start(ctx context.Context) error {
	ctx, r.cancel = context.WithCancel(ctx)

	r.wg.Add(2)

	go func() {
		defer r.wg.Done()
		r.pollLoop(ctx)
	}()

	go func() {
		defer r.wg.Done()
		r.cleanupLoop(ctx)
	}()

	slog.Info("outbox relay: started",
		slog.Duration("poll_interval", r.config.PollInterval),
		slog.Int("batch_size", r.config.BatchSize),
	)

	<-ctx.Done()
	// Graceful stop via Stop() cancels the context. Return nil to signal
	// clean exit per the worker.Worker contract (non-nil = abnormal).
	return nil
}

// Stop signals the relay to shut down gracefully and waits for goroutines.
// It respects the caller's context deadline: if ctx expires before goroutines
// finish, Stop returns an error instead of blocking indefinitely.
func (r *OutboxRelay) Stop(ctx context.Context) error {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

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

// pollOnce fetches a batch of unpublished entries within an explicit
// transaction (required for FOR UPDATE SKIP LOCKED) and publishes them.
// Successfully published entries are marked as published and committed;
// on any failure the transaction is rolled back.
func (r *OutboxRelay) pollOnce(ctx context.Context) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGConnect, "outbox relay: begin tx failed", err)
	}

	committed := false
	defer func() {
		if !committed {
			// Use context.WithoutCancel so rollback succeeds even when
			// the caller context has been cancelled.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	const fetchQuery = `SELECT id, aggregate_id, aggregate_type, event_type,
		topic, payload, metadata, created_at
		FROM outbox_entries
		WHERE published = false
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, fetchQuery, r.config.BatchSize)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: fetch query failed", err)
	}
	defer rows.Close()

	var entries []outbox.Entry
	for rows.Next() {
		var (
			e            outbox.Entry
			metadataJSON []byte
		)
		if scanErr := rows.Scan(
			&e.ID, &e.AggregateID, &e.AggregateType, &e.EventType,
			&e.Topic, &e.Payload, &metadataJSON, &e.CreatedAt,
		); scanErr != nil {
			return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: scan failed", scanErr)
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
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: rows iteration failed", rows.Err())
	}

	for _, e := range entries {
		payload, err := json.Marshal(e)
		if err != nil {
			slog.Error("outbox relay: marshal entry failed",
				slog.String("entry_id", e.ID),
				slog.Any("error", err),
			)
			continue
		}

		if err := r.pub.Publish(ctx, e.RoutingTopic(), payload); err != nil {
			slog.Error("outbox relay: publish failed",
				slog.String("entry_id", e.ID),
				slog.String("topic", e.RoutingTopic()),
				slog.Any("error", err),
			)
			// Do NOT mark as published; will retry on next poll.
			continue
		}

		const markQuery = `UPDATE outbox_entries SET published = true, published_at = now() WHERE id = $1`
		if _, err := tx.Exec(ctx, markQuery, e.ID); err != nil {
			slog.Error("outbox relay: mark published failed",
				slog.String("entry_id", e.ID),
				slog.Any("error", err),
			)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return errcode.Wrap(ErrAdapterPGConnect, "outbox relay: commit tx failed", err)
	}
	committed = true

	return nil
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
func (r *OutboxRelay) deletePublishedBefore(ctx context.Context, before time.Time) error {
	const query = `DELETE FROM outbox_entries WHERE published = true AND published_at < $1`
	ct, err := r.db.Exec(ctx, query, before)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: delete old entries failed", err)
	}
	if ct.RowsAffected() > 0 {
		slog.Info("outbox relay: cleaned up old entries",
			slog.Int64("deleted", ct.RowsAffected()),
		)
	}
	return nil
}
