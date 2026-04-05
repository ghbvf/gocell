package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Compile-time interface checks.
var (
	_ outbox.Relay  = (*OutboxRelay)(nil)
	_ worker.Worker = (*OutboxRelay)(nil)
)

// RelayConfig holds configuration for the OutboxRelay.
type RelayConfig struct {
	PollInterval    time.Duration // default 1s
	BatchSize       int           // default 100
	RetentionPeriod time.Duration // default 72h
	CleanupInterval time.Duration // default 1h
}

// DefaultRelayConfig returns a RelayConfig with sensible defaults.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		PollInterval:    1 * time.Second,
		BatchSize:       100,
		RetentionPeriod: 72 * time.Hour,
		CleanupInterval: 1 * time.Hour,
	}
}

// OutboxRelay polls unpublished outbox entries from PostgreSQL and publishes
// them via the configured outbox.Publisher. It implements both outbox.Relay
// and worker.Worker so it can be registered with bootstrap.WithWorkers().
//
// ref: ThreeDotsLabs/watermill-sql — outbox polling pattern
// Adopted: FOR UPDATE SKIP LOCKED for concurrent-safe polling; batch processing.
// Deviated: standalone relay goroutine instead of Watermill's subscriber-based
// forwarder; includes retention cleanup; full Entry JSON serialization.
type OutboxRelay struct {
	pool      *Pool
	publisher outbox.Publisher
	cfg       RelayConfig

	done chan struct{}
	wg   sync.WaitGroup
}

// NewOutboxRelay creates a new OutboxRelay.
// publisher is the kernel outbox.Publisher interface (not a concrete type).
func NewOutboxRelay(pool *Pool, publisher outbox.Publisher, cfg RelayConfig) *OutboxRelay {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultRelayConfig().PollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultRelayConfig().BatchSize
	}
	if cfg.RetentionPeriod <= 0 {
		cfg.RetentionPeriod = DefaultRelayConfig().RetentionPeriod
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = DefaultRelayConfig().CleanupInterval
	}

	return &OutboxRelay{
		pool:      pool,
		publisher: publisher,
		cfg:       cfg,
		done:      make(chan struct{}),
	}
}

// Start begins the polling and cleanup goroutines. It blocks until ctx is
// cancelled or Stop is called.
func (r *OutboxRelay) Start(ctx context.Context) error {
	slog.Info("outbox relay started",
		slog.Duration("poll_interval", r.cfg.PollInterval),
		slog.Int("batch_size", r.cfg.BatchSize),
		slog.Duration("retention_period", r.cfg.RetentionPeriod),
	)

	r.wg.Add(1)
	go r.cleanupLoop(ctx)

	defer r.wg.Wait()
	return r.pollLoop(ctx)
}

// Stop signals the relay to shut down gracefully and waits for in-flight
// batch processing to complete.
func (r *OutboxRelay) Stop(_ context.Context) error {
	select {
	case <-r.done:
		// Already stopped.
	default:
		close(r.done)
	}
	r.wg.Wait()
	slog.Info("outbox relay stopped")
	return nil
}

// pollLoop runs the polling cycle at the configured interval.
func (r *OutboxRelay) pollLoop(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.done:
			return nil
		case <-ticker.C:
			if err := r.processBatch(ctx); err != nil {
				slog.Error("outbox relay: batch processing failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// processBatch fetches a batch of unpublished entries, publishes each one,
// and marks successfully published entries.
func (r *OutboxRelay) processBatch(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: begin tx", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			slog.Error("outbox relay: rollback failed", slog.Any("error", rbErr))
		}
	}()

	entries, err := r.fetchUnpublished(ctx, tx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	published := 0
	for i := range entries {
		entry := &entries[i]

		payload, marshalErr := r.serializeEntry(entry)
		if marshalErr != nil {
			slog.Error("outbox relay: failed to serialize entry",
				slog.String("entry_id", entry.ID),
				slog.Any("error", marshalErr),
			)
			continue
		}

		if pubErr := r.publisher.Publish(ctx, entry.EventType, payload); pubErr != nil {
			slog.Warn("outbox relay: publish failed, entry will be retried",
				slog.String("entry_id", entry.ID),
				slog.String("event_type", entry.EventType),
				slog.Any("error", pubErr),
			)
			// Do NOT mark as published; it will be retried on the next poll.
			continue
		}

		if markErr := r.markPublished(ctx, tx, entry.ID); markErr != nil {
			slog.Error("outbox relay: failed to mark entry as published",
				slog.String("entry_id", entry.ID),
				slog.Any("error", markErr),
			)
			continue
		}
		published++
	}

	if err := tx.Commit(); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "outbox relay: commit tx", err)
	}

	if published > 0 {
		slog.Info("outbox relay: batch published",
			slog.Int("published", published),
			slog.Int("total", len(entries)),
		)
	}

	return nil
}

// fetchUnpublished selects unpublished entries using FOR UPDATE SKIP LOCKED
// to allow concurrent relay instances.
func (r *OutboxRelay) fetchUnpublished(ctx context.Context, tx *sql.Tx) ([]outbox.Entry, error) {
	const query = `SELECT id, aggregate_id, aggregate_type, event_type, payload, metadata, created_at
		FROM outbox_entries
		WHERE published = false
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.QueryContext(ctx, query, r.cfg.BatchSize)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: select unpublished", err)
	}
	defer rows.Close()

	var entries []outbox.Entry
	for rows.Next() {
		var (
			e            outbox.Entry
			metadataJSON []byte
		)
		if scanErr := rows.Scan(
			&e.ID,
			&e.AggregateID,
			&e.AggregateType,
			&e.EventType,
			&e.Payload,
			&metadataJSON,
			&e.CreatedAt,
		); scanErr != nil {
			return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: scan row", scanErr)
		}

		if len(metadataJSON) > 0 {
			if unmarshalErr := json.Unmarshal(metadataJSON, &e.Metadata); unmarshalErr != nil {
				slog.Warn("outbox relay: failed to unmarshal metadata, using empty map",
					slog.String("entry_id", e.ID),
					slog.Any("error", unmarshalErr),
				)
				e.Metadata = make(map[string]string)
			}
		}

		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox relay: rows iteration", err)
	}

	return entries, nil
}

// serializeEntry serializes the complete Entry (including ID, AggregateID,
// Metadata) to JSON for the publisher payload. This ensures downstream
// consumers receive the full outbox context.
func (r *OutboxRelay) serializeEntry(entry *outbox.Entry) ([]byte, error) {
	type entryPayload struct {
		ID            string            `json:"id"`
		AggregateID   string            `json:"aggregateId"`
		AggregateType string            `json:"aggregateType"`
		EventType     string            `json:"eventType"`
		Payload       json.RawMessage   `json:"payload"`
		Metadata      map[string]string `json:"metadata"`
		CreatedAt     time.Time         `json:"createdAt"`
	}

	ep := entryPayload{
		ID:            entry.ID,
		AggregateID:   entry.AggregateID,
		AggregateType: entry.AggregateType,
		EventType:     entry.EventType,
		Payload:       entry.Payload,
		Metadata:      entry.Metadata,
		CreatedAt:     entry.CreatedAt,
	}

	data, err := json.Marshal(ep)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGMarshal, fmt.Sprintf("outbox relay: marshal entry %s", entry.ID), err)
	}
	return data, nil
}

// markPublished updates a single entry's published flag and timestamp.
func (r *OutboxRelay) markPublished(ctx context.Context, tx *sql.Tx, entryID string) error {
	const query = `UPDATE outbox_entries SET published = true, published_at = now() WHERE id = $1`

	_, err := tx.ExecContext(ctx, query, entryID)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, fmt.Sprintf("outbox relay: mark published %s", entryID), err)
	}
	return nil
}

// cleanupLoop periodically deletes published entries older than the retention period.
func (r *OutboxRelay) cleanupLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case <-ticker.C:
			r.cleanup(ctx)
		}
	}
}

// cleanup deletes published entries older than the retention period.
func (r *OutboxRelay) cleanup(ctx context.Context) {
	const query = `DELETE FROM outbox_entries WHERE published = true AND published_at < now() - $1::interval`

	result, err := r.pool.ExecContext(ctx, query, r.cfg.RetentionPeriod.String())
	if err != nil {
		slog.Error("outbox relay: cleanup failed",
			slog.Any("error", err),
		)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Info("outbox relay: cleanup completed",
			slog.Int64("deleted", rowsAffected),
		)
	}
}
