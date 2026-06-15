package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
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

// projectionJournalReadySQL asserts the serving role still holds BOTH table-level
// privileges the durable journal depends on: SELECT (rebuild replay reads) and INSERT
// (the emit-time same-transaction double-write that appends every projection-source
// event, journaling_outbox_writer.go). migration 058 grants the serving role SELECT +
// INSERT and REVOKEs UPDATE/DELETE; a GRANT drift that strips INSERT would otherwise
// leave readyz green while every session-creating write fails on the journal append —
// the exact fail-open the prior `SELECT 1 ... WHERE false` probe could not see (it only
// proved SELECT + table existence). has_table_privilege additionally raises when
// projection_events is absent, so this one round-trip still surfaces the schema/migration
// drift a pool-level ping cannot detect (matches audit_ledger / session_store).
const projectionJournalReadySQL = `SELECT
	has_table_privilege(current_user, 'projection_events', 'SELECT'),
	has_table_privilege(current_user, 'projection_events', 'INSERT')`

// projectionEventPositionByIDSQL resolves a live-delivered entry's journal position by its
// id. This is the live-carrier resolution lookup (ResolveCarrier): unlike the transient-outbox
// path it replaced, it queries the never-cleaned projection_events journal, so a row committed
// by the D4 emit-time double-write before delivery is always present (a missing row is a genuine
// permanent error, never the spurious cleaned-row ErrNoRows that was the #1504 root bug).
const projectionEventPositionByIDSQL = `SELECT global_seq FROM projection_events WHERE id = $1`

// projectionEventLookupTimeout bounds the ResolveCarrier id→global_seq lookup. It mirrors the
// retired PGProjectionCursor.cursorPositionTimeout (5s): a stalled DB must surface as a
// (transient) timeout the Coordinator requeues, never hang the projection worker indefinitely.
// The lookup is an indexed unique-key read, so this is a backstop, not a hot-path budget; a
// PG-side statement_timeout remains a valid additional defense. context.WithTimeout honors a
// shorter caller deadline, so a tighter delivery ctx still wins.
const projectionEventLookupTimeout = 5 * time.Second

// PGProjectionEventSource is the durable production projection.ReplaySource + projection.LiveCursor
// backed by the append-only projection_events journal (EPIC #1504). Unlike the outbox-backed
// replay source it replaced (deleted in PR-03), it reads its monotonic position from
// projection_events.global_seq — a never-deleted journal — so a position can never depend
// on a row the relay may have deleted (the #1504 root bug: a cleaned outbox row →
// SELECT seq WHERE id=$1 → ErrNoRows → permanent error → dead-letter).
//
// Position reads global_seq off the JournalEvent carrier that Replay produced, with NO SQL
// lookup. That structurally closes the REBUILD-from-0 gap, because Replay itself constructs the
// position-bearing carrier from each row. The LIVE-path gap is closed by ResolveCarrier: the
// live broker delivers a raw outbox.Entry that carries no global_seq, which PositionFromCarrier
// would reject as a permanent error, so the Coordinator resolves it at the delivery boundary via
// ResolveCarrier into a position-bearing JournalEvent before Position is consulted (this source
// is wired as the Coordinator's projection.LiveCursor). The durable journal makes that resolution
// safe: the D4 same-transaction double-write commits the projection_events row before the event
// is delivered, and the journal is never cleaned, so the lookup cannot spuriously ErrNoRows (the
// exact failure mode that broke the transient-outbox path). The live-carrier protocol is recorded
// in ADR 202606071600-1504 §4.2.
//
// It holds a pgexec.PGExecutor (the sealed pool funnel, PG-REPO-AMBIENT-TX-01) rather than a
// raw pool. Replay is NOT transactional: the Coordinator invokes it outside RunInTx and wraps
// each fn callback in its own transaction. The SAME instance is wired as both the
// Coordinator's ReplaySource and its LiveCursor so the global_seq encoding is consistent across
// the interfaces (mirrors SagaJournalSource).
//
// ref: AxonFramework JdbcEventStore + TrackingToken — retained event store as projection source.
type PGProjectionEventSource struct {
	db pgexec.PGExecutor
}

// compile-time interface checks: one type is a ReplaySource, a full LiveCursor (Position +
// ResolveCarrier — the Coordinator's required cursor contract), and the differentiated
// repo-readiness probe.
var (
	_ projection.ReplaySource = (*PGProjectionEventSource)(nil)
	_ projection.LiveCursor   = (*PGProjectionEventSource)(nil)
	_ healthz.RepoProber      = (*PGProjectionEventSource)(nil)
)

// NewProjectionEventSource wraps the pool in the sealed pgexec funnel. A nil pool is
// rejected with ErrValidationFailed (same shape as NewProjectionCheckpointStore).
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

// ResolveCarrier normalizes a live-delivered entry into a position-bearing carrier
// (projection.LiveCarrierResolver). An already-positioned *JournalEvent is returned unchanged
// (idempotent — a rebuild carrier needs no lookup). A bare live outbox.Entry is resolved by
// querying its global_seq from the durable journal and wrapping it in a JournalEvent, so the
// Coordinator's carrier-intrinsic Position then succeeds.
//
// Error semantics mirror the position lookup the transient-outbox source used, but against the
// never-cleaned journal: pgx.ErrNoRows is a PERMANENT error (an entry genuinely absent from the
// journal cannot be assigned a position and retry cannot fix it — the D4 emit-time double-write
// guarantees a real projection-source event was committed before delivery, so a miss is a true
// invariant violation, not the spurious cleaned-row case); any other query failure is transient
// (wrapped, requeued).
//
// Transaction visibility: the lookup runs on the caller's ambient tx ctx (pgexec routes Query to
// the in-flight tx when one is present). The projection_events row was committed by the producer's
// D4 same-transaction double-write in a PRIOR transaction, before this event was delivered, so it
// is visible regardless of the txRunner's isolation level (it predates the consumer tx snapshot
// under REPEATABLE READ/SERIALIZABLE just as it does under READ COMMITTED). The lookup is read-only;
// it neither needs nor takes a separate connection. It is bounded by projectionEventLookupTimeout
// (deriving a child ctx that preserves the ambient tx value, so a stall surfaces as a transient
// timeout the Coordinator requeues rather than hanging the worker; a shorter caller deadline wins).
//
// Bootstrap-gap caveat (ADR 202606071600-1504 §5/§8): events produced BEFORE the PR-02 journaling
// decorator was deployed have no projection_events row, so a redelivery of such a historical event
// resolves to ErrNoRows → PERMANENT → dead-letter. That is the documented v1 limitation, not a
// system fault — the journal intentionally retains only events from the decorator's deployment
// forward, so such a DLX entry is expected and benign, not a recoverable transient.
func (s *PGProjectionEventSource) ResolveCarrier(
	ctx context.Context, entry projection.ProjectionEvent,
) (projection.ProjectionEvent, error) {
	if _, ok := entry.(*projection.JournalEvent); ok {
		return entry, nil
	}
	base, ok := entry.(kout.Entry)
	if !ok {
		return nil, kout.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection event source: ResolveCarrier requires a live outbox.Entry or a *JournalEvent carrier"))
	}
	lookupCtx, cancel := context.WithTimeout(ctx, projectionEventLookupTimeout)
	defer cancel()
	var globalSeq int64
	err := s.db.QueryRow(lookupCtx, projectionEventPositionByIDSQL, entry.EventID()).Scan(&globalSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, kout.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection event source: live entry not present in the projection_events journal"))
	}
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection event source: resolve live carrier", err)
	}
	return projection.NewJournalEvent(base, globalSeq), nil
}

// RepoReady implements healthz.RepoProber. It issues a cheap non-transactional privilege
// catalog read against projection_events so schema/migration drift and table-level permission
// loss surface as a differentiated failure domain distinct from the pool-level postgres_ready
// probe. A missing SELECT *or* INSERT grant fails the probe closed — INSERT is the production
// write capability the emit-time journal double-write needs, so a SELECT-only probe would stay
// green through an INSERT-stripping GRANT drift that silently breaks every session-creating
// write. Health handler contexts carry no pgx.Tx, so pgexec routes directly to the pool.
func (s *PGProjectionEventSource) RepoReady(ctx context.Context) error {
	var canSelect, canInsert bool
	if err := s.db.QueryRow(ctx, projectionJournalReadySQL).Scan(&canSelect, &canInsert); err != nil {
		// has_table_privilege raises on an absent relation, so a dropped/un-migrated
		// projection_events lands here (schema drift), distinct from a permission gap below.
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection journal: repo ready", err)
	}
	if !canSelect || !canInsert {
		return errcode.New(errcode.KindInternal, ErrAdapterPGSchemaShape,
			"projection journal: serving role lacks SELECT+INSERT on projection_events; migration 058 "+
				"must have run granting the serving role both (SELECT for rebuild replay, INSERT for the "+
				"emit-time journal double-write) — a stripped INSERT breaks every session-creating write")
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
