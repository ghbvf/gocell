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

// upsertCheckpointSQL advances the committed offset. The owner column is
// DELIBERATELY ABSENT from both the column list and the SET clause: v1 reserves
// it for v1.1 multi-pod pessimistic claim but never reads or writes it (ADR §Q5).
// PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01 statically guards this.
const upsertCheckpointSQL = `INSERT INTO projection_checkpoints (cell_id, projection_id, offset_seq, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (cell_id, projection_id)
DO UPDATE SET offset_seq = EXCLUDED.offset_seq, updated_at = NOW()`

// ProjectionCheckpointStore is the PostgreSQL projection.CheckpointStore backend
// for the CQRS projection lifecycle harness (epic #1100). It holds no raw pool:
// the sole field is a pgexec.PGExecutor that routes every statement through the
// ambient transaction carried by ctx (persistence.TxFromContext) when present,
// or the pool directly when not. In production the harness Coordinator wraps
// LoadOffset → apply → SaveOffset in one TxRunner.RunInTx, so all three run in
// the caller's transaction and the offset advance commits atomically with the
// Apply mutation (exactly-once). The bare-ctx pool path is exercised only by the
// shared projectiontest.RunCheckpointConformance harness, which drives each call
// outside a transaction.
//
// Holding pgexec.PGExecutor (not *pgxpool.Pool) is enforced by archtest
// PROJECTION-CHECKPOINT-TX-BOUND-01; the pool stays sealed in internal/pgexec
// (PG-REPO-AMBIENT-TX-01 R1/R2).
//
// ref: AxonFramework JdbcTokenStore — same-transaction checkpoint commit.
// ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — transactional offset upsert.
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3.
type ProjectionCheckpointStore struct {
	db pgexec.PGExecutor
}

// compile-time interface check.
var _ projection.CheckpointStore = (*ProjectionCheckpointStore)(nil)

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
// when ctx carries one (the exactly-once path); the owner column is never written.
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
