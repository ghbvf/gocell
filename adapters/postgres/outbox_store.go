package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metautil"
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

// maxPrincipalJSONBytes bounds the JSONB payload size accepted from the
// principal column at scan time. The principal family is sized identically to
// observability (4 idutil.SafeID fields), so it shares the same headroom
// formula and ~4 KB ceiling. The symmetry is required, not incidental:
// observability and principal are sibling reconstruction inputs and the
// scan-side size guard must be applied uniformly to both (issue #1229 review
// F5 — principal previously decoded without a cap, unlike observability).
const maxPrincipalJSONBytes = 4 * kout.MaxPrincipalTotalSize

// maxMetadataJSONBytes bounds the JSONB payload size accepted from the business
// metadata column at scan time. Producers cap total metadata at
// metautil.MaxMetadataTotalSize; the 4× headroom mirrors the observability /
// principal formula (JSON-encoding overhead — key names, quotes, separators —
// plus slack). Metadata is an untyped business KV map with no Validate(), so
// it cannot share decodeOversizeGuardedJSONB, but the same unbounded-allocation
// defense against a corrupted or maliciously-crafted row applies, so the cap is
// enforced inline below.
const maxMetadataJSONBytes = 4 * metautil.MaxMetadataTotalSize

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
// $1 kout.StateClaiming.String(), $2 kout.StatePending.String(), $3 batchSize, $4 leaseID (UUID).
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
		e.lease_id, e.principal, e.occurred_at,
		picked.next_retry_at AS picked_next_retry_at,
		picked.created_at AS picked_created_at
)
SELECT id, aggregate_id, aggregate_type, event_type,
	topic, payload, metadata, created_at, attempts, observability, lease_id, principal, occurred_at
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
// LIMIT ($8 batchSize) caps a single sweep so a large set of stale rows cannot
// produce a multi-second UPDATE that blocks VACUUM/replication; the relay's
// reclaim loop drains residual by re-invoking until count < batchSize.
//
// ref: graphile/worker resetLockedAt.ts — outer UPDATE re-asserts locked_by
// ref: river_job.sql / pgxjob — CTE + SKIP LOCKED batched reclaim
//
// $1 claimTTL interval text, $2 maxAttempts, $3 kout.StateDead.String(), $4 kout.StatePending.String(),
// $5 baseDelayMicros, $6 kout.StateClaiming.String(), $7 maxDelayMicros, $8 batchSize.
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

// countPendingQuery counts rows eligible for ClaimPending: status=pending AND
// (next_retry_at IS NULL OR next_retry_at <= now()). Rows still in backoff
// (next_retry_at > now()) are excluded, consistent with the ClaimPending
// eligibility predicate. May be approximate under high concurrency; callers
// must tolerate transient errors as non-fatal.
// status='pending' is a closed-set enum value; bound via $1 parameter (no string interpolation).
const countPendingQuery = `SELECT count(*) FROM outbox_entries WHERE status = $1 AND (next_retry_at IS NULL OR next_retry_at <= now())`

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
	rows, err := tx.Query(ctx, claimPendingQuery, kout.StateClaiming.String(), kout.StatePending.String(), batchSize, leaseID)
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
	ct, err := s.db.Exec(ctx, markPublishedQuery, kout.StatePublished.String(), id, kout.StateClaiming.String(), leaseID)
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
		kout.StatePending.String(), attempts, delayInterval, errMsg, id, kout.StateClaiming.String(), leaseID)
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
		kout.StateDead.String(), attempts, errMsg, id, kout.StateClaiming.String(), leaseID)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: MarkDead failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// ReclaimStale transitions up to batchSize claiming rows whose claimed_at is
// older than claimTTL back to pending (with attempts+1 and next_retry_at =
// backoff) or to dead (when attempts+1 >= maxAttempts). Returns count of rows
// recovered. The caller's reclaim loop re-runs to drain residual when the
// row count exceeds batchSize (`count < batchSize` signals "no more").
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
		kout.StateDead.String(), kout.StatePending.String(),
		baseDelay.Microseconds(), kout.StateClaiming.String(),
		maxDelay.Microseconds(), batchSize)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: ReclaimStale failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupPublished deletes a batch of published rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupPublished(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupPublishedQuery, kout.StatePublished.String(), cutoff, batchSize)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: CleanupPublished failed", err)
	}
	return int(ct.RowsAffected()), nil
}

// CleanupDead deletes a batch of dead rows older than cutoff.
// Caller is responsible for looping until deleted < batchSize.
func (s *PGOutboxStore) CleanupDead(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	ct, err := s.db.Exec(ctx, cleanupDeadQuery, kout.StateDead.String(), cutoff, batchSize)
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
//	metadata, created_at, attempts, observability, lease_id, principal, occurred_at
//
// metadata, observability, and principal are JSONB; NULL is valid for
// observability (treated as zero struct) and metadata (treated as empty map).
// principal is NOT NULL but may decode to a zero PrincipalMetadata when
// all fields are absent from the JSON object.
// A JSON parse failure is logged as Warn (data integrity); the entry is
// still returned with the affected field at its zero value.
// lease_id is returned by claim as a non-NULL UUID and surfaced as a string
// fencing token to the runtime layer.
func scanClaimedEntry(rows RowScanner) (outbox.ClaimedEntry, error) {
	var (
		scan              kout.EntryScan
		metadataJSON      []byte
		observabilityJSON []byte
		principalJSON     []byte
		leaseID           uuid.UUID
		attempts          int
	)
	if err := rows.Scan(
		&scan.ID, &scan.AggregateID, &scan.AggregateType, &scan.EventType,
		&scan.Topic, &scan.Payload, &metadataJSON, &scan.CreatedAt, &attempts,
		&observabilityJSON, &leaseID, &principalJSON, &scan.OccurredAt,
	); err != nil {
		return outbox.ClaimedEntry{}, err
	}

	if len(metadataJSON) > maxMetadataJSONBytes {
		// Defensive: reject oversized metadata payloads to prevent unbounded
		// allocation from a corrupted or maliciously-crafted row — same threat
		// the observability/principal caps defend against. Metadata is untyped
		// business KV (no Validate()), so this stays inline rather than routing
		// through decodeOversizeGuardedJSONB.
		slog.Warn("outbox store: metadata JSON exceeds max size, dropping",
			slog.String("entry_id", scan.ID),
			slog.String("event_type", scan.EventType),
			slog.Int("size", len(metadataJSON)),
			slog.Int("max", maxMetadataJSONBytes))
	} else if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &scan.Metadata); err != nil {
			slog.Warn("outbox store: failed to unmarshal metadata",
				slog.String("entry_id", scan.ID),
				slog.String("event_type", scan.EventType),
				slog.Int("size", len(metadataJSON)),
				slog.Any("error", err))
		}
	}

	if obs, ok := decodeOversizeGuardedJSONB[kout.ObservabilityMetadata](
		observabilityJSON, maxObservabilityJSONBytes, observabilityDecodeWarnings, scan.ID, scan.EventType,
	); ok {
		scan.Observability = obs
	}

	if principal, ok := decodeOversizeGuardedJSONB[kout.PrincipalMetadata](
		principalJSON, maxPrincipalJSONBytes, principalDecodeWarnings, scan.ID, scan.EventType,
	); ok {
		scan.Principal = principal
	}

	entry, err := scan.ToEntry()
	if err != nil {
		return outbox.ClaimedEntry{}, fmt.Errorf("outbox store: scanClaimedEntry: ToEntry: %w", err)
	}
	return outbox.ClaimedEntry{
		Entry:    entry,
		Attempts: attempts,
		LeaseID:  leaseID.String(),
	}, nil
}

// jsonbDecodeWarnings holds the three drop-reason Warn messages for one
// size-capped JSONB column. The messages are kept as const literals (declared
// per-column below) rather than runtime-concatenated from a field name, so the
// exact text stays greppable in source and stable for log-based alerting.
type jsonbDecodeWarnings struct {
	oversize       string // raw exceeds the column's byte cap
	unmarshalError string // json.Unmarshal failed
	validateError  string // decoded value failed Validate()
}

var (
	observabilityDecodeWarnings = jsonbDecodeWarnings{
		oversize:       "outbox store: observability JSON exceeds max size, dropping",
		unmarshalError: "outbox store: failed to unmarshal observability — dropping",
		validateError:  "outbox store: observability fails validation — dropping",
	}
	principalDecodeWarnings = jsonbDecodeWarnings{
		oversize:       "outbox store: principal JSON exceeds max size, dropping",
		unmarshalError: "outbox store: failed to unmarshal principal — dropping",
		validateError:  "outbox store: principal fails validation — dropping",
	}
)

// decodeOversizeGuardedJSONB decodes a size-capped, Validate-guarded JSONB
// column into T. It returns (zero, false) — leaving the caller's destination
// field at its zero value — when raw is empty, exceeds maxBytes, fails to
// unmarshal, or fails T.Validate(); each rejection is logged at Warn (using the
// caller-supplied per-column messages) with the entry/event identifiers. The
// staged decode into a local variable (PR #582 round-3 review F4) prevents a
// partial decode from leaking into the caller's field: json.Unmarshal is
// documented to leave partial values in the dst when it errors mid-decode (e.g.
// SafeID.UnmarshalJSON rejects field N while fields 1..N-1 already succeeded).
// Mirrors etcd clientv3 / K8s runtime.Decode staged-decode pattern. Shared by
// the observability and principal read-side guards, which are structurally
// identical (issue #1229 review F5: symmetric size caps prevent unbounded
// allocation from corrupted or maliciously-crafted rows).
//
// The metadata column is deliberately NOT routed through this helper: it is an
// untyped business KV map with no Validate() method, so it cannot satisfy the
// type constraint. Its size cap (maxMetadataJSONBytes) and simpler unmarshal
// stay inline above.
func decodeOversizeGuardedJSONB[T interface{ Validate() error }](
	raw []byte, maxBytes int, warn jsonbDecodeWarnings, entryID, eventType string,
) (T, bool) {
	var zero T
	if len(raw) == 0 {
		return zero, false
	}
	if len(raw) > maxBytes {
		slog.Warn(warn.oversize,
			slog.String("entry_id", entryID),
			slog.String("event_type", eventType),
			slog.Int("size", len(raw)),
			slog.Int("max", maxBytes))
		return zero, false
	}
	var decoded T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		slog.Warn(warn.unmarshalError,
			slog.String("entry_id", entryID),
			slog.String("event_type", eventType),
			slog.Int("size", len(raw)),
			slog.Any("error", err))
		return zero, false
	}
	if err := decoded.Validate(); err != nil {
		slog.Warn(warn.validateError,
			slog.String("entry_id", entryID),
			slog.String("event_type", eventType),
			slog.Int("size", len(raw)),
			slog.Any("error", err))
		return zero, false
	}
	return decoded, true
}

// CountPending returns the number of rows in pending status. The count may be
// approximate under high concurrency; callers should treat errors as transient
// and skip the metric update rather than panicking.
func (s *PGOutboxStore) CountPending(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRow(ctx, countPendingQuery, kout.StatePending.String()).Scan(&n); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: CountPending failed", err)
	}
	return n, nil
}

// OldestEligibleAt returns the oldest published_at (status=kout.StatePublished)
// or dead_at (status=kout.StateDead) in the table. Used by the relay's
// data-driven cleanup loop to schedule the next wake-up at oldest+retention
// instead of a fixed timer.
//
// status MUST be kout.StatePublished or kout.StateDead. Other values return an
// error immediately.
func (s *PGOutboxStore) OldestEligibleAt(ctx context.Context, status kout.State) (time.Time, bool, error) {
	var col string
	switch status {
	case kout.StatePublished:
		col = "published_at"
	case kout.StateDead:
		col = "dead_at"
	default:
		return time.Time{}, false, errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
			"OldestEligibleAt: invalid status",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("status=%s want StatePublished or StateDead", status))))
	}

	// The column name cannot be parameterised; the status value is bound via $1.
	query := fmt.Sprintf("SELECT MIN(%s) FROM outbox_entries WHERE status = $1", col)
	var oldest *time.Time
	if err := s.db.QueryRow(ctx, query, status.String()).Scan(&oldest); err != nil {
		return time.Time{}, false, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "outbox store: OldestEligibleAt failed", err)
	}
	if oldest == nil {
		return time.Time{}, false, nil
	}
	return *oldest, true, nil
}
