package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface check.
var _ outbox.Relay = (*OutboxRelay)(nil)

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

// OutboxRelay polls unpublished outbox entries from PostgreSQL and publishes
// them via the provided outbox.Publisher. Entries are marked as published only
// after successful delivery.
//
// ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — polling relay
// Adopted: SELECT ... FOR UPDATE SKIP LOCKED for concurrent-safe polling.
// Deviated: separate cleanup goroutine for retention; signal-first polling
// pattern instead of Watermill's subscription-based model.
type OutboxRelay struct {
	db     DBTX
	pub    outbox.Publisher
	config RelayConfig

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewOutboxRelay creates an OutboxRelay that polls from db and publishes via pub.
func NewOutboxRelay(db DBTX, pub outbox.Publisher, cfg RelayConfig) *OutboxRelay {
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
	return ctx.Err()
}

// Stop signals the relay to shut down gracefully and waits for goroutines.
func (r *OutboxRelay) Stop(_ context.Context) error {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
	r.wg.Wait()
	slog.Info("outbox relay: stopped")
	return nil
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

// pollOnce fetches a batch of unpublished entries and publishes them.
func (r *OutboxRelay) pollOnce(ctx context.Context) error {
	const fetchQuery = `SELECT id, aggregate_id, aggregate_type, event_type,
		payload, metadata, created_at
		FROM outbox_entries
		WHERE published = false
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := r.db.Query(ctx, fetchQuery, r.config.BatchSize)
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
		if err := rows.Scan(
			&e.ID, &e.AggregateID, &e.AggregateType, &e.EventType,
			&e.Payload, &metadataJSON, &e.CreatedAt,
		); err != nil {
			return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: scan failed", err)
		}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &e.Metadata); err != nil {
				slog.Warn("outbox relay: metadata unmarshal failed",
					slog.String("entry_id", e.ID),
					slog.Any("error", err),
				)
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: rows iteration failed", err)
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

		if err := r.pub.Publish(ctx, e.EventType, payload); err != nil {
			slog.Error("outbox relay: publish failed",
				slog.String("entry_id", e.ID),
				slog.String("event_type", e.EventType),
				slog.Any("error", err),
			)
			// Do NOT mark as published; will retry on next poll.
			continue
		}

		if err := r.markPublished(ctx, e.ID); err != nil {
			slog.Error("outbox relay: mark published failed",
				slog.String("entry_id", e.ID),
				slog.Any("error", err),
			)
		}
	}

	return nil
}

// markPublished sets an entry's published flag to true.
func (r *OutboxRelay) markPublished(ctx context.Context, id string) error {
	const query = `UPDATE outbox_entries SET published = true WHERE id = $1`
	_, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery,
			fmt.Sprintf("outbox relay: mark published failed for %s", id), err)
	}
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
	const query = `DELETE FROM outbox_entries WHERE published = true AND created_at < $1`
	affected, err := r.db.Exec(ctx, query, before)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: delete old entries failed", err)
	}
	if affected > 0 {
		slog.Info("outbox relay: cleaned up old entries",
			slog.Int64("deleted", affected),
		)
	}
	return nil
}
