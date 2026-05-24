//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/persistence/persistencetest"
)

// TestTxManager_AfterCommitConformance runs the shared after-commit conformance
// suite against the real PG-backed TxManager (top-level Begin/Commit path).
func TestTxManager_AfterCommitConformance(t *testing.T) {
	persistencetest.RunAfterCommitConformance(t, NewTxManager(emptyPool(t)))
}

// TestTxManager_AfterCommitFiresAfterDurableCommit asserts hooks observe a
// durable commit: a hook reading the row count sees the committed write, and a
// rolled-back tx fires no hook. This exercises the real Begin → Commit →
// drain ordering that the mockTx unit tests cannot.
func TestTxManager_AfterCommitFiresAfterDurableCommit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	_, err := pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS ac_test (
		id   SERIAL PRIMARY KEY,
		name TEXT NOT NULL
	)`)
	require.NoError(t, err)

	txm := NewTxManager(pool)

	t.Run("hook_sees_durable_row", func(t *testing.T) {
		var countAtHook int
		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			// Insert through the TRANSACTION (not the raw pool): the row is
			// uncommitted until RunInTx commits. The hook reads via pool.DB()
			// (a separate connection) so it observes the row only if the hook
			// fires after the durable commit — that is the ordering under test.
			tx, ok := persistence.TxFromContext[pgx.Tx](txCtx)
			require.True(t, ok, "tx must be in context")
			if _, e := tx.Exec(txCtx, "INSERT INTO ac_test (name) VALUES ($1)", "durable"); e != nil {
				return e
			}
			persistence.RegisterAfterCommit(txCtx, func(hookCtx context.Context) {
				_ = pool.DB().QueryRow(hookCtx,
					"SELECT count(*) FROM ac_test WHERE name = $1", "durable").Scan(&countAtHook)
			})
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 1, countAtHook, "hook runs after the commit is durable")
	})

	t.Run("no_hook_on_rollback", func(t *testing.T) {
		sentinel := errors.New("rollback please")
		fired := false
		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			persistence.RegisterAfterCommit(txCtx, func(context.Context) { fired = true })
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
		require.False(t, fired, "no after-commit hook fires on a rolled-back tx")
	})
}
