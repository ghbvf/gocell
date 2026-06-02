package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// replayHeadSQL returns the highest assigned stream position, or 0 when the
// journal is empty (the cold-start sentinel shared with CheckpointStore).
const replayHeadSQL = `SELECT COALESCE(MAX(seq), 0) FROM outbox_entries`

// replayScanSQL streams entries with position strictly greater than $1 in
// ascending position (seq) order. seq is the leading column so each row carries
// its position; the remaining columns mirror the relay claim projection minus the
// relay-internal attempts/lease_id (replay does not need delivery state).
const replayScanSQL = `SELECT seq, id, aggregate_id, aggregate_type, event_type,
	topic, payload, metadata, created_at, observability, principal, occurred_at
FROM outbox_entries
WHERE seq > $1
ORDER BY seq`

// cursorPositionSQL resolves a single entry's monotonic stream position by its
// UUID. This is the ONLY seq-by-id query in the package — PGProjectionCursor
// delegates here rather than issuing its own SQL, so the cursor and the replay
// source can never disagree about a row's position (the exactly-once basis).
const cursorPositionSQL = `SELECT seq FROM outbox_entries WHERE id = $1`

// PGProjectionReplaySource is the production projection.ReplaySource backed by the
// outbox journal (outbox_entries). The monotonic position is the migration-047
// `seq BIGINT GENERATED ALWAYS AS IDENTITY` column; deletions by the relay's
// CleanupPublished/CleanupDead create gaps, which the projection Cursor contract
// tolerates (gap-allowed). It holds a pgexec.PGExecutor (the sealed pool funnel,
// PG-REPO-AMBIENT-TX-01 R1/R2) rather than a raw pool.
//
// Replay is NOT transactional: the Coordinator invokes it outside RunInTx, so the
// ambient ctx carries no transaction and pgexec routes Query straight to the pool;
// the Coordinator wraps each fn callback in its own RunInTx separately.
//
// # Retention boundary (documented v1 limitation)
//
// The outbox is a transient relay — published/dead rows are eventually deleted by
// cleanup. A full rebuild-from-0 therefore replays only un-cleaned history;
// live/catch-up are unaffected. Faithful retention of projection-consumed events
// is a follow-up (backlog). See ADR
// docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
// §Amendment 2026-06-03 + #1368.
//
// ref: AxonFramework EventStore — ordered event stream replay by position/token.
type PGProjectionReplaySource struct {
	db pgexec.PGExecutor
}

// compile-time interface check.
var _ projection.ReplaySource = (*PGProjectionReplaySource)(nil)

// NewProjectionReplaySource wraps the pool in the sealed pgexec funnel. A nil pool
// is rejected with ErrValidationFailed (same shape as NewProjectionCheckpointStore).
func NewProjectionReplaySource(pool *pgxpool.Pool) (*PGProjectionReplaySource, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewProjectionReplaySource: pool must not be nil")
	}
	return &PGProjectionReplaySource{db: pgexec.New(pool)}, nil
}

// Head returns the highest available stream position, or 0 if the journal is
// empty. Used to compute the rebuild cutoff and the pending_events metric.
func (s *PGProjectionReplaySource) Head(ctx context.Context) (int64, error) {
	var head int64
	if err := s.db.QueryRow(ctx, replayHeadSQL).Scan(&head); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection replay source: head", err)
	}
	return head, nil
}

// Replay iterates entries with position strictly greater than fromOffset in
// ascending order, calling fn for each. fromOffset==0 replays all available
// history. If fn returns an error, Replay stops immediately and returns it;
// entries already passed to fn are NOT retried. ctx cancellation is honored
// between rows (pgx aborts the streaming query when ctx is done).
func (s *PGProjectionReplaySource) Replay(ctx context.Context, fromOffset int64, fn func(kout.Entry) error) error {
	rows, err := s.db.Query(ctx, replayScanSQL, fromOffset)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection replay source: query", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry, scanErr := scanReplayEntry(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection replay source: iterate", err)
	}
	return nil
}

// position resolves the seq of the entry with the given id. It is the single
// sanctioned seq-by-id query (PGProjectionCursor delegates here). A missing row
// yields a permanent error: an entry absent from the journal cannot be assigned a
// stream position and retry cannot fix it (cursor.go invariant #4). A genuine
// query failure is transient (wrapped, not permanent) so the Coordinator requeues.
func (s *PGProjectionReplaySource) position(ctx context.Context, id string) (int64, error) {
	var seq int64
	err := s.db.QueryRow(ctx, cursorPositionSQL, id).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, kout.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection cursor: entry not present in the outbox journal"))
	}
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection cursor: resolve position", err)
	}
	return seq, nil
}

// scanReplayEntry scans one replayScanSQL row (seq + entry columns) into a sealed
// Entry, reusing the shared applyEntryJSONB oversize guards. The leading seq
// column is read to advance the scan cursor but discarded: Replay delivers only
// the Entry, and the Coordinator resolves each event's position via the Cursor
// (which reads the same seq column), so the position is never threaded through fn.
func scanReplayEntry(rows RowScanner) (kout.Entry, error) {
	var (
		seq               int64
		scan              kout.EntryScan
		metadataJSON      []byte
		observabilityJSON []byte
		principalJSON     []byte
	)
	if err := rows.Scan(
		&seq, &scan.ID, &scan.AggregateID, &scan.AggregateType, &scan.EventType,
		&scan.Topic, &scan.Payload, &metadataJSON, &scan.CreatedAt,
		&observabilityJSON, &principalJSON, &scan.OccurredAt,
	); err != nil {
		return kout.Entry{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection replay source: scan", err)
	}
	applyEntryJSONB(&scan, metadataJSON, observabilityJSON, principalJSON)
	entry, err := scan.ToEntry()
	if err != nil {
		return kout.Entry{}, fmt.Errorf("projection replay source: scanReplayEntry: ToEntry: %w", err)
	}
	return entry, nil
}

// PGProjectionCursor is the production projection.Cursor backed by the outbox
// journal. It holds the paired PGProjectionReplaySource and delegates position
// resolution to it — the cursor declares NO SQL of its own, so the cursor and the
// replay source read the identical seq column and can never drift. This is the
// exactly-once foundation expressed structurally (single sanctioned seq-SQL
// holder), stronger than a "both read the same column" convention. Mirrors
// MemCursor wrapping MemReplaySource.
type PGProjectionCursor struct {
	src *PGProjectionReplaySource
}

// compile-time interface check.
var _ projection.Cursor = (*PGProjectionCursor)(nil)

// NewProjectionCursor returns a cursor backed by src. src is required: a nil
// source is rejected here (fail-fast) rather than deferred to the first Position
// call.
func NewProjectionCursor(src *PGProjectionReplaySource) (*PGProjectionCursor, error) {
	if src == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewProjectionCursor: src PGProjectionReplaySource is required")
	}
	return &PGProjectionCursor{src: src}, nil
}

// Position resolves the entry's monotonic stream position (its outbox_entries.seq)
// by delegating to the paired replay source. The projection.Cursor interface
// carries no ctx (frozen since PR-01), so a background context is used for the
// single indexed lookup; it runs against the pool (no ambient tx), which is sound
// because the entry's row was committed by its producer in a prior transaction.
func (c *PGProjectionCursor) Position(entry kout.Entry) (int64, error) {
	return c.src.position(context.Background(), entry.ID())
}
