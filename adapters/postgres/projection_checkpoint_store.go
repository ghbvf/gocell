package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// selectCheckpointSQL reads a projection's committed offset. A missing row
// (pgx.ErrNoRows) is the cold-start signal and maps to offset 0.
const selectCheckpointSQL = `SELECT offset_seq FROM projection_checkpoints WHERE cell_id = $1 AND projection_id = $2`

// selectCheckpointForUpdateSQL reads both the committed offset and the current
// owner for an AdvanceIfOwner CAS, locking the row for the duration of the
// ambient transaction (prevents concurrent AdvanceIfOwner from reading stale
// state between the read and the write).
const selectCheckpointForUpdateSQL = `SELECT offset_seq, owner FROM projection_checkpoints WHERE cell_id = $1 AND projection_id = $2 FOR UPDATE` //nolint:lll // SQL literal must stay a single static string (B1 blind-spot constraint — dynamic construction escapes the owner-write scan)

// insertCheckpointWithOwnerSQL performs the cold-claim insert: used only when
// no checkpoint row exists yet AND the requested offset is strictly ahead of
// the virtual cold row {owner:"", offset:0}. SaveOffset (upsertCheckpointSQL)
// never writes the owner column, so the OwnerCheckpointStore contract
// (AdvanceIfOwner) is the sole write path for owner.
const insertCheckpointWithOwnerSQL = `INSERT INTO projection_checkpoints (cell_id, projection_id, offset_seq, owner, updated_at)
VALUES ($1, $2, $3, $4, NOW())`

// updateCheckpointWithOwnerSQL applies a conditional CAS advance for an
// existing checkpoint row: sets offset_seq and owner unconditionally because
// the read-then-write logic in AdvanceIfOwner already validated the fencing
// predicate (same-owner OR strictly-ahead) before calling this statement.
const updateCheckpointWithOwnerSQL = `UPDATE projection_checkpoints SET offset_seq = $3, owner = $4, updated_at = NOW()
WHERE cell_id = $1 AND projection_id = $2`

// upsertCheckpointSQL advances the committed offset. The owner column is
// DELIBERATELY ABSENT from both the column list and the SET clause: SaveOffset
// is the unconditional base CheckpointStore contract used by the outbox
// projection Coordinator. Because the write omits owner, its NOT NULL
// constraint is satisfied solely by the column's empty-string default
// (asserted at startup by schema_guard.verifyDefaults). The fenced
// OwnerCheckpointStore.AdvanceIfOwner write path uses insertCheckpointWithOwnerSQL
// and updateCheckpointWithOwnerSQL, which DO include owner.
const upsertCheckpointSQL = `INSERT INTO projection_checkpoints (cell_id, projection_id, offset_seq, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (cell_id, projection_id)
DO UPDATE SET offset_seq = EXCLUDED.offset_seq, updated_at = NOW()`

// ProjectionCheckpointStore is the PostgreSQL projection.CheckpointStore and
// projection.OwnerCheckpointStore backend for the CQRS projection lifecycle
// harness (epic #1100 / #1609). It holds no raw pool: the sole field is a
// pgexec.PGExecutor that routes every statement through the ambient transaction
// carried by ctx (persistence.TxFromContext) when present, or the pool
// directly when not.
//
// CheckpointStore (SaveOffset) path: the harness Coordinator wraps
// LoadOffset → apply → SaveOffset in one TxRunner.RunInTx, so all three run
// in the caller's transaction and the offset advance commits atomically with
// the Apply mutation (exactly-once).
//
// OwnerCheckpointStore (AdvanceIfOwner) path: the saga Tailer wraps its
// apply + AdvanceIfOwner in one TxRunner.RunInTx so the fenced checkpoint
// advance commits atomically with the Apply mutation (D5(a) / D5(b)). The
// CAS uses a read-FOR UPDATE then conditional write within the ambient tx to
// prevent concurrent handoff races at the SQL layer — see AdvanceIfOwner.
//
// Holding pgexec.PGExecutor (not *pgxpool.Pool) is enforced by archtest
// PROJECTION-CHECKPOINT-TX-BOUND-01; the pool stays sealed in internal/pgexec
// (PG-REPO-AMBIENT-TX-01 R1/R2).
//
// ref: AxonFramework JdbcTokenStore — same-transaction checkpoint commit.
// ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — transactional offset upsert.
// ref: JasperFx/marten EventStore Async Daemon pg_advisory_lock + token claim.
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3/§Q5.
// ref: docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md §D5(b).
type ProjectionCheckpointStore struct {
	db pgexec.PGExecutor
}

// compile-time interface checks.
var (
	_ projection.CheckpointStore      = (*ProjectionCheckpointStore)(nil)
	_ projection.OwnerCheckpointStore = (*ProjectionCheckpointStore)(nil)
)

// NewProjectionCheckpointStore wraps the pool in the sealed pgexec funnel
// (PG-REPO-AMBIENT-TX-01 R2). A nil pool is rejected with ErrValidationFailed.
func NewProjectionCheckpointStore(pool *pgxpool.Pool) (*ProjectionCheckpointStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewProjectionCheckpointStore: pool must not be nil")
	}
	return &ProjectionCheckpointStore{db: pgexec.New(pool)}, nil
}

// LoadOffset returns the committed offset for (cellID, projectionID), or 0 if no
// row exists yet (cold start). It runs within the ambient transaction when ctx
// carries one.
//
// cellID and projectionID are opaque caller-owned UTF-8 keys stored verbatim as
// the TEXT primary key; the no-dash projectionID convention is the caller's
// (cellgen's) responsibility, not validated here. On query failure the error
// carries cell_id/projection_id as server-only internal detail so ops can locate
// the failing projection from slog without exposing the keys on the wire.
func (s *ProjectionCheckpointStore) LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error) {
	var offset int64
	err := s.db.QueryRow(ctx, selectCheckpointSQL, cellID, projectionID).Scan(&offset)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection checkpoint store: load offset", err,
			errcode.WithInternal(
				errcode.InternalAttr("cell_id", cellID),
				errcode.InternalAttr("projection_id", projectionID),
			))
	}
	return offset, nil
}

// SaveOffset advances the projection's committed offset, upserting on the
// (cell_id, projection_id) primary key. It runs within the ambient transaction
// when ctx carries one (the exactly-once path); the owner column is never
// written (base CheckpointStore contract — AdvanceIfOwner is the fenced path).
// On failure the error carries cell_id/projection_id as server-only internal
// detail for ops correlation.
func (s *ProjectionCheckpointStore) SaveOffset(ctx context.Context, cellID, projectionID string, offset int64) error {
	if _, err := s.db.Exec(ctx, upsertCheckpointSQL, cellID, projectionID, offset); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection checkpoint store: save offset", err,
			errcode.WithInternal(
				errcode.InternalAttr("cell_id", cellID),
				errcode.InternalAttr("projection_id", projectionID),
			))
	}
	return nil
}

// AdvanceIfOwner advances the committed offset fenced by ownerToken (semantics B,
// per the projection.OwnerCheckpointStore interface godoc — the authoritative
// spec is projectiontest.RunOwnerCheckpointConformance).
//
// Fencing logic (mirrors MemOwnerCheckpointStore, verified by the shared
// conformance harness):
//   - empty ownerToken → KindInvalid (fail-closed; a real Tailer always mints
//     a non-empty UUID per lock acquisition)
//   - no existing row AND offset > 0 → INSERT (cold claim)
//   - no existing row AND offset == 0 → ErrStaleOwner (0 is not strictly ahead
//     of the virtual cold committed offset 0)
//   - existing row AND (ownerToken == recorded owner OR offset > recorded offset)
//     → UPDATE (accept: same leader re-advance or new leader claiming ahead)
//   - existing row otherwise → ErrStaleOwner (different token, not ahead)
//
// The read uses SELECT … FOR UPDATE so that concurrent AdvanceIfOwner calls
// within the same leader-handoff window are serialized at the SQL layer: the
// second caller blocks on the lock, then re-reads the row written by the first
// (which may have changed the owner and/or offset), and the fencing predicate
// is re-evaluated against the fresh state. This makes the CAS race-free
// without requiring an optimistic retry loop in the application layer.
//
// AdvanceIfOwner participates in the ambient transaction via s.db
// (pgexec.PGExecutor, PROJECTION-CHECKPOINT-TX-BOUND-01): both the SELECT FOR
// UPDATE and the INSERT/UPDATE see and join the caller's transaction, so the
// offset advance commits atomically with the Apply mutation (D5(a) + D5(b)).
func (s *ProjectionCheckpointStore) AdvanceIfOwner(ctx context.Context, cellID, projectionID, ownerToken string, offset int64) error {
	if ownerToken == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection checkpoint store: AdvanceIfOwner requires a non-empty owner token")
	}

	var recordedOffset int64
	var recordedOwner string
	err := s.db.QueryRow(ctx, selectCheckpointForUpdateSQL, cellID, projectionID).Scan(&recordedOffset, &recordedOwner)

	if errors.Is(err, pgx.ErrNoRows) {
		// Cold start: virtual row is {owner:"", offset:0}.
		// Accept only if offset > 0 (strictly ahead of 0).
		if offset <= 0 {
			return projection.ErrStaleOwner
		}
		if _, insertErr := s.db.Exec(ctx, insertCheckpointWithOwnerSQL, cellID, projectionID, offset, ownerToken); insertErr != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
				"projection checkpoint store: advance if owner (cold insert)", insertErr,
				errcode.WithInternal(
					errcode.InternalAttr("cell_id", cellID),
					errcode.InternalAttr("projection_id", projectionID),
				))
		}
		return nil
	}
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection checkpoint store: advance if owner (read)", err,
			errcode.WithInternal(
				errcode.InternalAttr("cell_id", cellID),
				errcode.InternalAttr("projection_id", projectionID),
			))
	}

	// Row exists: apply fencing predicate (semantics B).
	if ownerToken != recordedOwner && offset <= recordedOffset {
		return projection.ErrStaleOwner
	}

	if _, updateErr := s.db.Exec(ctx, updateCheckpointWithOwnerSQL, cellID, projectionID, offset, ownerToken); updateErr != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"projection checkpoint store: advance if owner (update)", updateErr,
			errcode.WithInternal(
				errcode.InternalAttr("cell_id", cellID),
				errcode.InternalAttr("projection_id", projectionID),
			))
	}
	return nil
}
