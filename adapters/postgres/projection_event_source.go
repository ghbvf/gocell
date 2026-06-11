package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/kernel/healthz"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ProbeProjectionJournalReady is the typed readyz probe name for the durable
// projection_events journal (PROBENAME-SEALED-FUNNEL-01: adapter probe names are
// declared typed consts ending in _ready; bare strings never reach RegisterReadiness).
// The composition root registers it against PGProjectionEventSource.RepoReady when the
// durable source is wired (EPIC #1504 PR-03); PR-01 ships the capability + conformance.
const ProbeProjectionJournalReady healthz.ProbeName = "projection_journal_ready"

// projectionEventHeadSQL returns the highest assigned global_seq, or 0 when the journal is
// empty (the cold-start sentinel shared with CheckpointStore.LoadOffset).
const projectionEventHeadSQL = `SELECT COALESCE(MAX(global_seq), 0) FROM projection_events`

// projectionEventReplaySQL streams events with global_seq strictly greater than $1 in
// ascending position order. global_seq is the leading column so each row carries its
// position; the remaining columns mirror the EntryScan rebuild set in the same order as
// the relay claim projection (applyEntryJSONB is shared).
const projectionEventReplaySQL = `SELECT global_seq, id, aggregate_id, aggregate_type, event_type,
	topic, payload, metadata, created_at, observability, principal, occurred_at
FROM projection_events
WHERE global_seq > $1
ORDER BY global_seq`

// projectionJournalReadySQL is a representative zero-cost query against projection_events.
// It returns no rows but exercises schema existence and table-level permissions, surfacing
// migration drift a pool-level ping cannot detect (matches audit_ledger / session_store).
const projectionJournalReadySQL = `SELECT 1 FROM projection_events WHERE false`

// PGProjectionEventSource is the durable production projection.ReplaySource + projection.Cursor
// backed by the append-only projection_events journal (EPIC #1504). Unlike the
// outbox-backed PGProjectionReplaySource it replaces (PR-03), it reads its monotonic position
// from projection_events.global_seq — a never-deleted journal — so a position can never depend
// on a row the relay may have deleted (the #1504 root bug: a cleaned outbox row →
// SELECT seq WHERE id=$1 → ErrNoRows → permanent error → dead-letter).
//
// Position reads global_seq off the JournalEvent carrier that Replay produced, with NO SQL
// lookup. That structurally closes the REBUILD-from-0 gap, because Replay itself constructs the
// position-bearing carrier from each row. The LIVE-path gap is NOT closed by carrier-intrinsic
// Position alone: the live broker delivers a raw outbox.Entry that carries no global_seq, which
// PositionFromCarrier rejects as a permanent error. Closing the live path needs the wiring that
// makes this source the Coordinator's live Cursor (PR-03) to hand the Cursor a position-bearing
// carrier — resolving the entry's journal global_seq at the delivery boundary. The durable
// journal is what makes that resolution safe: the D4 same-transaction double-write commits the
// projection_events row before the event is delivered, and the journal is never cleaned, so the
// resolution cannot ErrNoRows (the exact failure mode that broke the transient-outbox path).
// PR-01 ships the rebuild source + the carrier-read Position replay relies on; the live-carrier
// resolver and its regression coverage land in PR-03 (the durable-source wiring). The live-carrier
// protocol is recorded in ADR 202606071600-1504 §4.2.
//
// It holds a pgexec.PGExecutor (the sealed pool funnel, PG-REPO-AMBIENT-TX-01) rather than a
// raw pool. Replay is NOT transactional: the Coordinator invokes it outside RunInTx and wraps
// each fn callback in its own transaction. The SAME instance is wired as both the
// Coordinator's ReplaySource and its Cursor so the global_seq encoding is consistent across
// the two interfaces (mirrors SagaJournalSource).
//
// ref: AxonFramework JdbcEventStore + TrackingToken — retained event store as projection source.
type PGProjectionEventSource struct {
	db pgexec.PGExecutor
}

// compile-time interface checks: one type satisfies both projection contracts and the
// differentiated repo-readiness probe.
var (
	_ projection.ReplaySource = (*PGProjectionEventSource)(nil)
	_ projection.Cursor       = (*PGProjectionEventSource)(nil)
	_ healthz.RepoProber      = (*PGProjectionEventSource)(nil)
)

// NewProjectionEventSource wraps the pool in the sealed pgexec funnel. A nil pool is
// rejected with ErrValidationFailed (same shape as NewProjectionReplaySource).
func NewProjectionEventSource(pool *pgxpool.Pool) (*PGProjectionEventSource, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewProjectionEventSource: pool must not be nil")
	}
	return &PGProjectionEventSource{db: pgexec.New(pool)}, nil
}

// Head returns the highest available global_seq, or 0 if the journal is empty. Used to
// compute the rebuild cutoff and the pending_events metric.
func (s *PGProjectionEventSource) Head(ctx context.Context) (int64, error) {
	var head int64
	if err := s.db.QueryRow(ctx, projectionEventHeadSQL).Scan(&head); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection event source: head", err)
	}
	return head, nil
}

// Replay iterates events with global_seq strictly greater than fromOffset in ascending order,
// calling fn for each. fromOffset==0 replays all history. If fn returns an error, Replay stops
// immediately and returns it; events already passed to fn are NOT retried. ctx cancellation is
// honored between rows (pgx aborts the streaming query when ctx is done). Each row is rebuilt
// through EntryScan.ToEntry and wrapped in a projection.JournalEvent carrying its global_seq.
func (s *PGProjectionEventSource) Replay(ctx context.Context, fromOffset int64, fn func(projection.ProjectionEvent) error) error {
	rows, err := s.db.Query(ctx, projectionEventReplaySQL, fromOffset)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection event source: query", err)
	}
	defer rows.Close()
	for rows.Next() {
		carrier, scanErr := scanProjectionEvent(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := fn(carrier); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection event source: iterate", err)
	}
	return nil
}

// Position resolves the event's monotonic stream position by reading the global_seq off the
// JournalEvent carrier that Replay produced — issuing NO SQL. This is the #1504 structural
// fix: the position can never depend on a projection_events row (and a fortiori never on a
// deleted transient outbox row). An unresolvable carrier is permanent (Cursor invariant #4),
// delegated to the shared projection.PositionFromCarrier.
func (s *PGProjectionEventSource) Position(entry projection.ProjectionEvent) (int64, error) {
	return projection.PositionFromCarrier(entry)
}

// RepoReady implements healthz.RepoProber. It issues a cheap non-transactional representative
// query against projection_events so schema/migration drift and table-level permission loss
// surface as a differentiated failure domain distinct from the pool-level postgres_ready probe.
// Health handler contexts carry no pgx.Tx, so pgexec routes directly to the pool.
func (s *PGProjectionEventSource) RepoReady(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, projectionJournalReadySQL); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection journal: repo ready", err)
	}
	return nil
}

// scanProjectionEvent scans one projectionEventReplaySQL row (global_seq + entry columns) into
// a sealed Entry via the shared EntryScan funnel + applyEntryJSONB oversize guards, then wraps
// it with its global_seq as the durable journal carrier. The leading global_seq column is read
// both to advance the scan cursor AND threaded into the carrier — that is the whole point: the
// Cursor later reads it off the carrier instead of re-querying the row.
func scanProjectionEvent(rows RowScanner) (*projection.JournalEvent, error) {
	var (
		globalSeq         int64
		scan              kout.EntryScan
		metadataJSON      []byte
		observabilityJSON []byte
		principalJSON     []byte
	)
	if err := rows.Scan(
		&globalSeq, &scan.ID, &scan.AggregateID, &scan.AggregateType, &scan.EventType,
		&scan.Topic, &scan.Payload, &metadataJSON, &scan.CreatedAt,
		&observabilityJSON, &principalJSON, &scan.OccurredAt,
	); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection event source: scan", err)
	}
	applyEntryJSONB(&scan, metadataJSON, observabilityJSON, principalJSON)
	entry, err := scan.ToEntry()
	if err != nil {
		return nil, fmt.Errorf("projection event source: scanProjectionEvent: ToEntry: %w", err)
	}
	return projection.NewJournalEvent(entry, globalSeq), nil
}
