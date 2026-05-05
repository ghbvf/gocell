package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// maxObservabilityJSONBytes bounds the JSONB payload size accepted from the
// observability column at scan time. Sized to ~4× MaxObservabilityTotalSize
// so JSON-encoding overhead (key names, quotes) plus future field additions
// have headroom while still capping unbounded allocations from a corrupted
// or maliciously-crafted row at ~4 KB.
const maxObservabilityJSONBytes = 4 * kout.MaxObservabilityTotalSize

// PGOutboxStore implements runtime/outbox.Store over PostgreSQL using pgx.
//
// Each method opens its own short transaction; methods do not compose into a
// larger transaction. The backing DB handle is typically a *pgxpool.Pool.
//
// Consistency level: L2 (OutboxFact) — adapts the outbox state machine from
// the relay layer into discrete, testable DB operations.
type PGOutboxStore struct {
	db    relayDB // same interface used by OutboxRelay — Exec/Query/Begin
	clock clock.Clock
}

// Compile-time assertion.
var _ outbox.Store = (*PGOutboxStore)(nil)

// NewOutboxStore constructs a Store backed by the supplied database handle.
// The handle is typically a *pgxpool.Pool; it must support short-lived
// transactions (Begin).
func NewOutboxStore(db relayDB, clk clock.Clock) *PGOutboxStore {
	clock.MustHaveClock(clk, "postgres.NewOutboxStore")
	return &PGOutboxStore{db: db, clock: clk}
}

// ---------------------------------------------------------------------------
// SQL constants — sourced from outbox_relay.go (claim / writeBack / reclaimStale
// / deletePublishedBefore). Kept as named constants to keep method bodies below
// the cognitive-complexity ceiling.
// ---------------------------------------------------------------------------

// claimPendingQuery first materializes the rows selected for this claim, then
// updates them with a fresh lease_id (the fencing token), then returns the
// updated entries ordered by the materialized selection keys. UPDATE ...
// RETURNING does not guarantee row order by itself, so the final ORDER BY is
// required for durable delivery order.
//
// ORDER BY matches idx_outbox_pending (next_retry_at NULLS FIRST, created_at)
// with id as a stable tie-breaker for rows with identical timestamps.
//
// $1 statusClaiming, $2 statusPending, $3 batchSize, $4 leaseID (UUID).
//
// ref: graphile/worker sql/000001.sql get_job — locked_by SET on claim
// ref: jackc/pgxjob pgxjob.go — worker_id UUID via CTE
const claimPendingQuery = `WITH picked AS MATERIALIZED (
	SELECT id, next_retry_at, created_at
	FROM outbox_entries
	WHERE status = $2
		AND (next_retry_at IS NULL OR next_retry_at <= now())
	ORDER BY next_retry_at NULLS FIRST, created_at, id
	LIMIT $3
	FOR UPDATE SKIP LOCKED
),
updated AS (
	UPDATE outbox_entries AS e
	SET status = $1, claimed_at = now(), lease_id = $4::uuid
	FROM picked
	WHERE e.id = picked.id
	RETURNING e.id, e.aggregate_id, e.aggregate_type, e.event_type,
		e.topic, e.payload, e.metadata, e.created_at, e.attempts, e.observability,
		e.lease_id,
		picked.next_retry_at AS picked_next_retry_at,
		picked.created_at AS picked_created_at
)
SELECT id, aggregate_id, aggregate_type, event_type,
	topic, payload, metadata, created_at, attempts, observability, lease_id
FROM updated
ORDER BY picked_next_retry_at NULLS FIRST, picked_created_at, id`

// markPublishedQuery transitions claiming → published with fencing CAS:
// row matches only when the stored lease_id equals the lease the caller was
// granted at Claim time. A reclaimed (lease_id NULL) or re-leased row will
// miss; caller observes RowsAffected==0 and silently drops.
const markPublishedQuery = `UPDATE outbox_entries SET status = $1, published_at = now()
	WHERE id = $2 AND status = $3 AND lease_id = $4::uuid`

// markRetryQuery transitions claiming → pending with fencing CAS. lease_id is
// cleared (set to NULL) so the next ClaimPending must mint a fresh lease.
const markRetryQuery = `UPDATE outbox_entries SET status = $1, attempts = $2,
	next_retry_at = now() + $3, last_error = $4, lease_id = NULL
	WHERE id = $5 AND status = $6 AND lease_id = $7::uuid`

// markDeadQuery transitions claiming → dead with fencing CAS.
const markDeadQuery = `UPDATE outbox_entries SET status = $1, attempts = $2,
	last_error = $3, dead_at = now()
	WHERE id = $4 AND status = $5 AND lease_id = $6::uuid`

// reclaimStaleQuery sweeps claiming rows whose lease has expired. The CTE
// `picked` snapshots id+lease_id under FOR UPDATE SKIP LOCKED so concurrent
// MarkPublished/MarkRetry/MarkDead transactions never collide with reclaim;
// the outer UPDATE re-asserts status=$6 AND lease_id=picked.lease_id so a row
// that left 'claiming' (or had its lease rotated) between the SELECT and the
// UPDATE is skipped instead of regressing to pending.
//
// lease_id is cleared on the back-to-pending branch so the next ClaimPending
// mints a fresh lease; the dead branch keeps the value as audit trail.
//
// LIMIT ($8 batchSize) caps a single sweep so a backlog of stale rows cannot
// produce a multi-second UPDATE that blocks VACUUM/replication; the relay's
// reclaim loop drains residual by re-invoking until count < batchSize.
//
// ref: graphile/worker resetLockedAt.ts — outer UPDATE re-asserts locked_by
// ref: river_job.sql / pgxjob — CTE + SKIP LOCKED batched reclaim
//
// $1 claimTTL interval text, $2 maxAttempts, $3 statusDead, $4 statusPending,
// $5 baseDelayMicros, $6 statusClaiming, $7 maxDelayMicros, $8 batchSize.
const reclaimStaleQuery = `WITH picked AS (
		SELECT id, lease_id, attempts FROM outbox_entries
		WHERE status = $6 AND claimed_at < now() - $1::interval
		ORDER BY claimed_at
		FOR UPDATE SKIP LOCKED
		LIMIT $8
	)
	UPDATE outbox_entries o
	SET status = CASE WHEN picked.attempts + 1 >= $2 THEN $3 ELSE $4 END,
		attempts = picked.attempts + 1,
		claimed_at = NULL,
		lease_id = CASE WHEN picked.attempts + 1 >= $2 THEN picked.lease_id ELSE NULL END,
		dead_at = CASE WHEN picked.attempts + 1 >= $2 THEN now() ELSE NULL END,
		next_retry_at = CASE WHEN picked.attempts + 1 >= $2 THEN NULL
			ELSE now() + LEAST($5 * power(2, picked.attempts + 1), $7) * interval '1 microsecond' END
	FROM picked
	WHERE o.id = picked.id
		AND o.status = $6
		AND o.lease_id = picked.lease_id`

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
// claiming status, stamping each with a fresh lease_id (UUID). The lease is
// the fencing token that callers MUST echo through subsequent Mark* calls;
// without it, an in-flight worker whose claim was already reclaimed cannot
// overwrite a new owner's outcome. Returns empty slice + nil when nothing is
// claimable.
func (s *PGOutboxStore) ClaimPending(ctx context.Context, batchSize int) ([]outbox.ClaimedEntry, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "outbox store: ClaimPending begin tx", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	leaseID := uuid.NewString()
	rows, err := tx.Query(ctx, claimPendingQuery, statusClaiming, statusPending, batchSize, leaseID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: ClaimPending query failed", err)
	}
	defer rows.Close()

	var entries []outbox.ClaimedEntry
	for rows.Next() {
		ce, scanErr := scanClaimedEntry(rows)
		if scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: ClaimPending scan failed", scanErr)
		}
		entries = append(entries, ce)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: ClaimPending rows iteration failed", rows.Err())
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "outbox store: ClaimPending commit failed", err)
	}
	committed = true

	return entries, nil
}

// MarkPublished transitions an entry from claiming to published, fencing on
// leaseID. updated=false means the lease was lost (ReclaimStale + re-Claim, or
// row already in a terminal state) — silent at-least-once OK; callers must
// not treat it as error.
func (s *PGOutboxStore) MarkPublished(ctx context.Context, id, leaseID string) (bool, error) {
	ct, err := s.db.Exec(ctx, markPublishedQuery, statusPublished, id, statusClaiming, leaseID)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: MarkPublished failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkRetry transitions a failing entry back to pending with the supplied
// nextRetryAt and attempts count, fencing on leaseID. updated=false same as
// MarkPublished.
func (s *PGOutboxStore) MarkRetry(
	ctx context.Context, id, leaseID string,
	attempts int, nextRetryAt time.Time, lastError string,
) (bool, error) {
	// Convert time.Time to a PG interval offset from now().
	// We use an absolute timestamp approach: compute delay from now, then
	// express as "N microseconds" interval added to now() in SQL.
	// This matches the writeBack approach: pass a duration interval string
	// (pgx serializes time.Duration as int64 nanoseconds which PG cannot cast
	// to interval directly — SQLSTATE 42846).
	delay := max(s.clock.Until(nextRetryAt), 0)
	delayInterval := fmt.Sprintf("%d microseconds", delay.Microseconds())

	errMsg := sanitizeError(lastError, 1000)

	ct, err := s.db.Exec(ctx, markRetryQuery,
		statusPending, attempts, delayInterval, errMsg, id, statusClaiming, leaseID)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: MarkRetry failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkDead transitions a failing entry to dead status, fencing on leaseID.
// updated=false when the lease was lost.
func (s *PGOutboxStore) MarkDead(ctx context.Context, id, leaseID string, attempts int, lastError string) (bool, error) {
	errMsg := sanitizeError(lastError, 1000)

	ct, err := s.db.Exec(ctx, markDeadQuery,
		statusDead, attempts, errMsg, id, statusClaiming, leaseID)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: MarkDead failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// ReclaimStale transitions up to batchSize claiming rows whose claimed_at is
// older than claimTTL back to pending (with attempts+1 and next_retry_at =
// backoff) or to dead (when attempts+1 >= maxAttempts). Returns count of rows
// recovered. The caller's reclaim loop re-runs to drain residual when the
// backlog exceeds batchSize (`count < batchSize` signals "no more").
func (s *PGOutboxStore) ReclaimStale(
	ctx context.Context,
	claimTTL time.Duration,
	maxAttempts int,
	baseDelay, maxDelay time.Duration,
	batchSize int,
) (int, error) {
	// pgx serializes time.Duration as int64 nanoseconds which PostgreSQL cannot
	// cast to interval (SQLSTATE 42846). Pass claimTTL as "N microseconds" text;
	// baseDelay and maxDelay as int64 microseconds multiplied by interval '1 microsecond'.
	claimTTLInterval := fmt.Sprintf("%d microseconds", claimTTL.Microseconds())

	ct, err := s.db.Exec(ctx, reclaimStaleQuery,
		claimTTLInterval, maxAttempts,
		statusDead, statusPending,
		baseDelay.Microseconds(), statusClaiming,
		maxDelay.Microseconds(), batchSize)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: ReclaimStale failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupPublished deletes a batch of published rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupPublished(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupPublishedQuery, statusPublished, cutoff, batchSize)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: CleanupPublished failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupDead deletes a batch of dead rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupDead(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupDeadQuery, statusDead, cutoff, batchSize)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: CleanupDead failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// ---------------------------------------------------------------------------
// Internal scan helpers
// ---------------------------------------------------------------------------

// scanClaimedEntry scans one row from claimPendingQuery RETURNING into a
// ClaimedEntry. Column order:
//
//	id, aggregate_id, aggregate_type, event_type, topic, payload,
//	metadata, created_at, attempts, observability, lease_id
//
// Both metadata and observability are JSONB; NULL is valid for both and is
// treated as an empty map / zero struct respectively. A JSON parse failure
// is logged as Warn (data integrity) and the entry is still returned.
// lease_id is returned by claim as a non-NULL UUID and surfaced as a string
// fencing token to the runtime layer.
func scanClaimedEntry(rows RowScanner) (outbox.ClaimedEntry, error) {
	var (
		ce                outbox.ClaimedEntry
		metadataJSON      []byte
		observabilityJSON []byte
		leaseID           uuid.UUID
	)
	if err := rows.Scan(
		&ce.ID, &ce.AggregateID, &ce.AggregateType, &ce.EventType,
		&ce.Topic, &ce.Payload, &metadataJSON, &ce.CreatedAt, &ce.Attempts,
		&observabilityJSON, &leaseID,
	); err != nil {
		return outbox.ClaimedEntry{}, err
	}
	ce.LeaseID = leaseID.String()
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &ce.Metadata); err != nil {
			slog.Warn("outbox store: failed to unmarshal metadata",
				slog.String("entry_id", ce.ID),
				slog.String("event_type", ce.EventType),
				slog.Any("error", err))
		}
	}
	if len(observabilityJSON) > maxObservabilityJSONBytes {
		// Defensive: reject oversized observability payloads to prevent
		// unbounded allocation from a corrupted row. Field-level limits
		// in ObservabilityMetadata.Validate cover the producer side; this
		// guard covers tampered/legacy data on the read side.
		slog.Warn("outbox store: observability JSON exceeds max size, dropping",
			slog.String("entry_id", ce.ID),
			slog.String("event_type", ce.EventType),
			slog.Int("size", len(observabilityJSON)),
			slog.Int("max", maxObservabilityJSONBytes))
	} else if len(observabilityJSON) > 0 {
		if err := json.Unmarshal(observabilityJSON, &ce.Observability); err != nil {
			slog.Warn("outbox store: failed to unmarshal observability",
				slog.String("entry_id", ce.ID),
				slog.String("event_type", ce.EventType),
				slog.Any("error", err))
		} else if validateErr := ce.Observability.Validate(); validateErr != nil {
			// Persisted row violates field-size invariants (older row written
			// before the invariant existed, or schema drift). Clear and warn —
			// downstream restore must not see partially valid IDs.
			slog.Warn("outbox store: observability fails validation, clearing",
				slog.String("entry_id", ce.ID),
				slog.String("event_type", ce.EventType),
				slog.Any("error", validateErr))
			ce.Observability = kout.ObservabilityMetadata{}
		}
	}
	return ce, nil
}

// OldestEligibleAt returns the oldest published_at (status="published") or
// dead_at (status="dead") in the table. Used by the relay's data-driven
// cleanup loop to schedule the next wake-up at oldest+retention instead of a
// fixed timer.
func (s *PGOutboxStore) OldestEligibleAt(ctx context.Context, status string) (time.Time, bool, error) {
	var col string
	switch status {
	case statusPublished:
		col = "published_at"
	case statusDead:
		col = "dead_at"
	default:
		return time.Time{}, false, errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
			fmt.Sprintf("OldestEligibleAt: invalid status %q (want published or dead)", status))
	}

	// Inline status as a literal (validated by the switch above) so we don't
	// need a placeholder for it; the column name cannot be parameterised.
	query := fmt.Sprintf("SELECT MIN(%s) FROM outbox_entries WHERE status = $1", col)
	var oldest *time.Time
	if err := s.db.QueryRow(ctx, query, status).Scan(&oldest); err != nil {
		return time.Time{}, false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: OldestEligibleAt failed", err)
	}
	if oldest == nil {
		return time.Time{}, false, nil
	}
	return *oldest, true, nil
}
