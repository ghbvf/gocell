package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// PGOutboxStore implements runtime/outbox.Store over PostgreSQL using pgx.
//
// Each method opens its own short transaction; methods do not compose into a
// larger transaction. The backing DB handle is typically a *pgxpool.Pool.
//
// Consistency level: L2 (OutboxFact) — adapts the outbox state machine from
// the relay layer into discrete, testable DB operations.
type PGOutboxStore struct {
	db relayDB // same interface used by OutboxRelay — Exec/Query/Begin
}

// Compile-time assertion.
var _ outbox.Store = (*PGOutboxStore)(nil)

// NewOutboxStore constructs a Store backed by the supplied database handle.
// The handle is typically a *pgxpool.Pool; it must support short-lived
// transactions (Begin).
func NewOutboxStore(db relayDB) *PGOutboxStore {
	return &PGOutboxStore{db: db}
}

// ---------------------------------------------------------------------------
// SQL constants — sourced from outbox_relay.go (claim / writeBack / reclaimStale
// / deletePublishedBefore). Kept as named constants to keep method bodies below
// the cognitive-complexity ceiling.
// ---------------------------------------------------------------------------

// claimPendingQuery is identical to the inner SQL of OutboxRelay.claim.
// ORDER BY matches idx_outbox_pending_v2 (next_retry_at NULLS FIRST, created_at)
// so PostgreSQL can use the partial index without an extra Sort step.
const claimPendingQuery = `UPDATE outbox_entries
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

// markPublishedQuery is identical to writeBackMarkPublished in outbox_relay.go.
const markPublishedQuery = `UPDATE outbox_entries SET status = $1, published_at = now()
	WHERE id = $2 AND status = $3`

// markRetryQuery is identical to writeBackMarkRetry in outbox_relay.go.
const markRetryQuery = `UPDATE outbox_entries SET status = $1, attempts = $2,
	next_retry_at = now() + $3, last_error = $4
	WHERE id = $5 AND status = $6`

// markDeadQuery is identical to writeBackMarkDead in outbox_relay.go.
const markDeadQuery = `UPDATE outbox_entries SET status = $1, attempts = $2, last_error = $3, dead_at = now()
	WHERE id = $4 AND status = $5`

// reclaimStaleQuery is identical to the SQL in OutboxRelay.reclaimStale.
// $1 claimTTL interval text, $2 maxAttempts, $3 statusDead, $4 statusPending,
// $5 baseDelayMicros, $6 statusClaiming, $7 maxDelayMicros.
const reclaimStaleQuery = `UPDATE outbox_entries
	SET status = CASE WHEN attempts + 1 >= $2 THEN $3 ELSE $4 END,
		attempts = attempts + 1,
		claimed_at = NULL,
		dead_at = CASE WHEN attempts + 1 >= $2 THEN now() ELSE NULL END,
		next_retry_at = CASE WHEN attempts + 1 >= $2 THEN NULL
			ELSE now() + LEAST($5 * power(2, attempts + 1), $7) * interval '1 microsecond' END
	WHERE status = $6 AND claimed_at < now() - $1::interval`

// cleanupPublishedQuery is identical to publishedQuery in OutboxRelay.deletePublishedBefore.
const cleanupPublishedQuery = `DELETE FROM outbox_entries WHERE id IN (
	SELECT id FROM outbox_entries WHERE status = $1 AND published_at < $2 LIMIT $3)`

// cleanupDeadQuery is identical to deadQuery in OutboxRelay.deletePublishedBefore.
const cleanupDeadQuery = `DELETE FROM outbox_entries WHERE id IN (
	SELECT id FROM outbox_entries WHERE status = $1 AND dead_at < $2 LIMIT $3)`

// ---------------------------------------------------------------------------
// Store method implementations
// ---------------------------------------------------------------------------

// ClaimPending atomically transitions up to batchSize rows from pending to
// claiming status. Returns empty slice + nil when nothing is claimable.
func (s *PGOutboxStore) ClaimPending(ctx context.Context, batchSize int) ([]outbox.ClaimedEntry, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "outbox store: ClaimPending begin tx", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	rows, err := tx.Query(ctx, claimPendingQuery, statusClaiming, statusPending, batchSize)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox store: ClaimPending query failed", err)
	}
	defer rows.Close()

	var entries []outbox.ClaimedEntry
	for rows.Next() {
		var (
			ce           outbox.ClaimedEntry
			metadataJSON []byte
		)
		if scanErr := rows.Scan(
			&ce.ID, &ce.AggregateID, &ce.AggregateType, &ce.EventType,
			&ce.Topic, &ce.Payload, &metadataJSON, &ce.CreatedAt, &ce.Attempts,
		); scanErr != nil {
			return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox store: ClaimPending scan failed", scanErr)
		}
		if len(metadataJSON) > 0 {
			if jsonErr := json.Unmarshal(metadataJSON, &ce.Metadata); jsonErr != nil {
				slog.Warn("outbox store: metadata unmarshal failed",
					slog.String("entry_id", ce.ID),
					slog.Any("error", jsonErr),
				)
			}
		}
		entries = append(entries, ce)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "outbox store: ClaimPending rows iteration failed", rows.Err())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "outbox store: ClaimPending commit failed", err)
	}
	committed = true

	return entries, nil
}

// MarkPublished transitions an entry from claiming to published.
// updated=false means the entry was reclaimed by ReclaimStale (not an error).
func (s *PGOutboxStore) MarkPublished(ctx context.Context, id string) (bool, error) {
	ct, err := s.db.Exec(ctx, markPublishedQuery, statusPublished, id, statusClaiming)
	if err != nil {
		return false, errcode.Wrap(ErrAdapterPGQuery, "outbox store: MarkPublished failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkRetry transitions a failing entry back to pending with the supplied
// nextRetryAt and attempts count. updated=false when entry no longer in claiming.
func (s *PGOutboxStore) MarkRetry(ctx context.Context, id string, attempts int, nextRetryAt time.Time, lastError string) (bool, error) {
	// Convert time.Time to a PG interval offset from now().
	// We use an absolute timestamp approach: compute delay from now, then
	// express as "N microseconds" interval added to now() in SQL.
	// This matches the writeBack approach: pass a duration interval string
	// (pgx serialises time.Duration as int64 nanoseconds which PG cannot cast
	// to interval directly — SQLSTATE 42846).
	delay := max(time.Until(nextRetryAt), 0)
	delayInterval := fmt.Sprintf("%d microseconds", delay.Microseconds())

	errMsg := sanitizeError(lastError, 1000)

	ct, err := s.db.Exec(ctx, markRetryQuery,
		statusPending, attempts, delayInterval, errMsg, id, statusClaiming)
	if err != nil {
		return false, errcode.Wrap(ErrAdapterPGQuery, "outbox store: MarkRetry failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkDead transitions a failing entry to dead status.
// updated=false when entry no longer in claiming.
func (s *PGOutboxStore) MarkDead(ctx context.Context, id string, attempts int, lastError string) (bool, error) {
	errMsg := sanitizeError(lastError, 1000)

	ct, err := s.db.Exec(ctx, markDeadQuery,
		statusDead, attempts, errMsg, id, statusClaiming)
	if err != nil {
		return false, errcode.Wrap(ErrAdapterPGQuery, "outbox store: MarkDead failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// ReclaimStale transitions claiming rows whose claimed_at is older than claimTTL
// back to pending (with attempts+1 and next_retry_at = backoff) or to dead
// (when attempts+1 >= maxAttempts). Returns count of rows recovered.
func (s *PGOutboxStore) ReclaimStale(ctx context.Context, claimTTL time.Duration, maxAttempts int, baseDelay, maxDelay time.Duration) (int, error) {
	// pgx serialises time.Duration as int64 nanoseconds which PostgreSQL cannot
	// cast to interval (SQLSTATE 42846). Pass claimTTL as "N microseconds" text;
	// baseDelay and maxDelay as int64 microseconds multiplied by interval '1 microsecond'.
	claimTTLInterval := fmt.Sprintf("%d microseconds", claimTTL.Microseconds())

	ct, err := s.db.Exec(ctx, reclaimStaleQuery,
		claimTTLInterval, maxAttempts,
		statusDead, statusPending,
		baseDelay.Microseconds(), statusClaiming,
		maxDelay.Microseconds())
	if err != nil {
		return 0, errcode.Wrap(ErrAdapterPGQuery, "outbox store: ReclaimStale failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupPublished deletes a batch of published rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupPublished(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupPublishedQuery, statusPublished, cutoff, batchSize)
	if err != nil {
		return 0, errcode.Wrap(ErrAdapterPGQuery, "outbox store: CleanupPublished failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupDead deletes a batch of dead rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupDead(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupDeadQuery, statusDead, cutoff, batchSize)
	if err != nil {
		return 0, errcode.Wrap(ErrAdapterPGQuery, "outbox store: CleanupDead failed", err)
	}
	return int(ct.RowsAffected()), nil
}
