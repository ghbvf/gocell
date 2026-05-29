//go:build archtest_fixture

// Package projectioncheckpointtxfixture is a synthetic violation fixture for
// PROJECTION-CHECKPOINT-TX-BOUND-01. It contains a fake CheckpointStore
// implementation that illegally holds a *pgxpool.Pool field directly, which
// the rule must detect.
//
// DO NOT use this package in production code.
package projectioncheckpointtxfixture

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/projection"
)

// badCheckpointStore is a fake projection.CheckpointStore that illegally
// holds a *pgxpool.Pool field instead of going through an ambient-tx funnel.
// VIOLATION: struct field of type *pgxpool.Pool in a CheckpointStore impl.
// PROJECTION-CHECKPOINT-TX-BOUND-01 must fire on the pool field.
type badCheckpointStore struct {
	pool *pgxpool.Pool // VIOLATION: raw *pgxpool.Pool field in CheckpointStore impl
}

// LoadOffset returns 0. Stub only — this store is for fixture testing.
func (b *badCheckpointStore) LoadOffset(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

// SaveOffset calls pool.Exec directly instead of using an ambient tx.
// VIOLATION: raw pool.Exec call bypasses ambient transaction.
func (b *badCheckpointStore) SaveOffset(ctx context.Context, cellID, projectionID string, offset int64) error {
	_, err := b.pool.Exec(ctx,
		"INSERT INTO projection_checkpoints(cell_id, projection_id, offset) VALUES($1,$2,$3) ON CONFLICT DO UPDATE SET offset=$3",
		cellID, projectionID, offset,
	)
	return err
}

// compile-time interface check.
var _ projection.CheckpointStore = (*badCheckpointStore)(nil)
