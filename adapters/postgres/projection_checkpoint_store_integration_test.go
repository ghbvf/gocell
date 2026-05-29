//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// newCheckpointStore builds a real PG-backed ProjectionCheckpointStore on a
// fresh per-test database cloned from the migrated template (table 045 present),
// plus a TxManager for the ambient-transaction tests.
func newCheckpointStore(t *testing.T) (*ProjectionCheckpointStore, *TxManager) {
	t.Helper()
	pool := migratedPool(t)
	store, err := NewProjectionCheckpointStore(pool.DB())
	require.NoError(t, err)
	return store, NewTxManager(pool)
}

// TestProjectionCheckpointStore_Conformance enrolls the concrete
// *ProjectionCheckpointStore in the shared CheckpointStore conformance harness
// (PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01). The harness drives Load/Save
// with bare context.Background(); pgexec routes to the pool, so each statement
// auto-commits — exercising the SELECT / INSERT…ON CONFLICT SQL against real PG.
func TestProjectionCheckpointStore_Conformance(t *testing.T) {
	store, _ := newCheckpointStore(t)
	projectiontest.RunCheckpointConformance(t, store)
}

// TestProjectionCheckpointStore_ColdStart verifies LoadOffset returns 0 for an
// unknown (cellID, projectionID) against an empty table.
func TestProjectionCheckpointStore_ColdStart(t *testing.T) {
	store, _ := newCheckpointStore(t)
	got, err := store.LoadOffset(context.Background(), "ordercell", "ordersummary")
	require.NoError(t, err)
	assert.Equal(t, int64(0), got)
}

// TestProjectionCheckpointStore_Upsert verifies the ON CONFLICT path updates the
// existing row in place (no duplicate-key error) and the latest offset wins.
func TestProjectionCheckpointStore_Upsert(t *testing.T) {
	store, _ := newCheckpointStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveOffset(ctx, "ordercell", "ordersummary", 5))
	require.NoError(t, store.SaveOffset(ctx, "ordercell", "ordersummary", 9))
	got, err := store.LoadOffset(ctx, "ordercell", "ordersummary")
	require.NoError(t, err)
	assert.Equal(t, int64(9), got, "ON CONFLICT must update offset_seq to the latest save")
}

// TestProjectionCheckpointStore_CommitReadYourWrite verifies that within a single
// RunInTx both SaveOffset and a subsequent LoadOffset see the ambient tx
// (read-your-write), and the advance is durable after commit.
func TestProjectionCheckpointStore_CommitReadYourWrite(t *testing.T) {
	store, txm := newCheckpointStore(t)
	ctx := context.Background()

	err := txm.RunInTx(ctx, func(txCtx context.Context) error {
		if e := store.SaveOffset(txCtx, "ordercell", "ordersummary", 3); e != nil {
			return e
		}
		got, e := store.LoadOffset(txCtx, "ordercell", "ordersummary")
		if e != nil {
			return e
		}
		assert.Equal(t, int64(3), got, "read-your-write: LoadOffset inside the tx must see the uncommitted SaveOffset")
		return nil
	})
	require.NoError(t, err)

	// After commit, a fresh (poolside) read sees the committed offset.
	got, err := store.LoadOffset(ctx, "ordercell", "ordersummary")
	require.NoError(t, err)
	assert.Equal(t, int64(3), got)
}

// TestProjectionCheckpointStore_RollbackIsAtomicWithSiblingWrite is the
// exactly-once cornerstone: SaveOffset participates in the caller's transaction,
// so a rollback discards BOTH the offset advance and a sibling business write.
// Neither must be visible afterward.
func TestProjectionCheckpointStore_RollbackIsAtomicWithSiblingWrite(t *testing.T) {
	pool := migratedPool(t)
	store, err := NewProjectionCheckpointStore(pool.DB())
	require.NoError(t, err)
	txm := NewTxManager(pool)
	ctx := context.Background()

	// Scratch "business read-model" table colocated in the checkpoint store's DB
	// so the offset advance and the sibling write share one transaction.
	_, err = pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS cp_atomicity_probe (id INT PRIMARY KEY)`)
	require.NoError(t, err)

	sentinel := errors.New("force rollback")
	runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		if e := store.SaveOffset(txCtx, "ordercell", "ordersummary", 7); e != nil {
			return e
		}
		// The sibling business write must join the SAME ambient tx — use the
		// tx from ctx (pool.DB().Exec would open its own connection and
		// auto-commit, defeating the atomicity this test asserts).
		//
		// require.True (not `return err`) is deliberate: a missing ambient tx is a
		// harness precondition failure, not the rollback this test forces. Returning
		// an error here would still satisfy require.ErrorIs(runErr, sentinel)? No —
		// it would NOT (different error), but a future refactor could make it a
		// false pass by also returning sentinel. Fail-fast here removes that risk.
		tx, ok := persistence.TxFromContext[pgx.Tx](txCtx)
		require.True(t, ok, "ambient tx must be present in RunInTx callback ctx")
		if _, e := tx.Exec(txCtx, `INSERT INTO cp_atomicity_probe (id) VALUES (1)`); e != nil {
			return e
		}
		return sentinel // abort → both writes roll back together
	})
	require.ErrorIs(t, runErr, sentinel)

	// Offset advance was rolled back.
	off, err := store.LoadOffset(ctx, "ordercell", "ordersummary")
	require.NoError(t, err)
	assert.Equal(t, int64(0), off, "SaveOffset must roll back with the aborted ambient tx")

	// Sibling business write was rolled back too (same transaction).
	var count int
	require.NoError(t, pool.DB().QueryRow(ctx, `SELECT count(*) FROM cp_atomicity_probe`).Scan(&count))
	assert.Equal(t, 0, count, "sibling write must roll back atomically with the checkpoint advance")
}
