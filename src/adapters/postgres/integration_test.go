//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupPostgres starts a PostgreSQL container via testcontainers and returns a
// connected Pool along with a cleanup function. The caller must invoke cleanup
// (or use t.Cleanup) to terminate the container.
func setupPostgres(t *testing.T) (*Pool, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:15-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "failed to get connection string")

	pool, err := NewPool(ctx, Config{DSN: connStr})
	require.NoError(t, err, "failed to create pool")

	cleanup := func() {
		pool.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate container: %v", err)
		}
	}

	return pool, cleanup
}

// ---------------------------------------------------------------------------
// T19: TestIntegration_Pool
// ---------------------------------------------------------------------------

// TestIntegration_Pool verifies that Pool can connect to a real PostgreSQL
// instance, pass the Health() probe, and shut down cleanly.
func TestIntegration_Pool(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	t.Run("connect_and_health", func(t *testing.T) {
		// Pool was already successfully created by setupPostgres.
		// Health() should return nil on a healthy connection.
		err := pool.Health(ctx)
		assert.NoError(t, err, "Health() should return nil on a connected pool")
	})

	t.Run("stats_non_empty", func(t *testing.T) {
		stats := pool.Stats()
		assert.NotEmpty(t, stats, "Stats() should return a non-empty string")
	})

	t.Run("close", func(t *testing.T) {
		// Close is idempotent; calling it should not panic. We do not call
		// Health() after Close() because pgxpool does not guarantee an error —
		// just verify Close() itself doesn't panic.
		assert.NotPanics(t, func() {
			pool.Close()
		}, "Close() should not panic")
	})
}

// ---------------------------------------------------------------------------
// T20: TestIntegration_TxManager
// ---------------------------------------------------------------------------

// TestIntegration_TxManager tests commit, rollback, and panic-recovery
// semantics of TxManager.RunInTx against a real PostgreSQL instance.
func TestIntegration_TxManager(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Create a simple test table.
	_, err := pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS tx_test (
		id   SERIAL PRIMARY KEY,
		name TEXT NOT NULL
	)`)
	require.NoError(t, err, "failed to create tx_test table")

	txm := NewTxManager(pool)

	t.Run("commit_path", func(t *testing.T) {
		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			tx, ok := TxFromContext(txCtx)
			require.True(t, ok, "transaction must be in context")

			_, err := tx.Exec(txCtx, "INSERT INTO tx_test (name) VALUES ($1)", "committed")
			return err
		})
		require.NoError(t, err, "RunInTx commit path should succeed")

		// Verify the row was committed.
		var count int
		err = pool.DB().QueryRow(ctx, "SELECT count(*) FROM tx_test WHERE name = $1", "committed").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "committed row should be visible")
	})

	t.Run("rollback_on_error", func(t *testing.T) {
		handlerErr := errors.New("simulated failure")
		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			tx, ok := TxFromContext(txCtx)
			require.True(t, ok)

			_, execErr := tx.Exec(txCtx, "INSERT INTO tx_test (name) VALUES ($1)", "rolled_back")
			require.NoError(t, execErr)

			return handlerErr
		})
		require.Error(t, err, "RunInTx should return handler error")
		assert.ErrorIs(t, err, handlerErr)

		// Verify the row was NOT committed (rolled back).
		var count int
		err = pool.DB().QueryRow(ctx, "SELECT count(*) FROM tx_test WHERE name = $1", "rolled_back").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "rolled-back row should not be visible")
	})

	t.Run("rollback_on_panic", func(t *testing.T) {
		assert.Panics(t, func() {
			_ = txm.RunInTx(ctx, func(txCtx context.Context) error {
				tx, ok := TxFromContext(txCtx)
				if !ok {
					return errors.New("no tx")
				}

				_, err := tx.Exec(txCtx, "INSERT INTO tx_test (name) VALUES ($1)", "panicked")
				if err != nil {
					return err
				}

				panic("simulated panic")
			})
		}, "RunInTx should re-panic after rollback")

		// Verify the row was NOT committed (rolled back before re-panic).
		var count int
		err := pool.DB().QueryRow(ctx, "SELECT count(*) FROM tx_test WHERE name = $1", "panicked").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "panicked row should not be visible after rollback")
	})
}

// ---------------------------------------------------------------------------
// T21: TestIntegration_Migrator
// ---------------------------------------------------------------------------

// TestIntegration_Migrator tests Up, Status, and Down against a real
// PostgreSQL instance using the embedded migration files.
func TestIntegration_Migrator(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator should succeed")

	t.Run("up", func(t *testing.T) {
		err := migrator.Up(ctx)
		require.NoError(t, err, "Up() should apply all migrations without error")

		// Verify the outbox_entries table was created by querying its schema.
		var exists bool
		err = pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "outbox_entries table should exist after Up()")
	})

	t.Run("up_idempotent", func(t *testing.T) {
		// Running Up() again should be a no-op (already applied).
		err := migrator.Up(ctx)
		assert.NoError(t, err, "Up() should be idempotent")
	})

	t.Run("status", func(t *testing.T) {
		statuses, err := migrator.Status(ctx)
		require.NoError(t, err, "Status() should succeed")
		require.NotEmpty(t, statuses, "Status() should return at least one migration")

		// The first migration should be 001_create_outbox_entries and Applied.
		assert.Equal(t, "001", statuses[0].Version)
		assert.Equal(t, "create_outbox_entries", statuses[0].Name)
		assert.True(t, statuses[0].Applied, "migration 001 should be marked as applied")
		assert.False(t, statuses[0].AppliedAt.IsZero(), "AppliedAt should be set")
	})

	t.Run("down", func(t *testing.T) {
		// First Down() rolls back 003 (status columns), table still exists.
		err := migrator.Down(ctx)
		require.NoError(t, err, "Down() should roll back migration 003")

		// Table still exists after rolling back only 003.
		var exists bool
		err = pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "outbox_entries table should still exist after rolling back 003")

		// Second Down() rolls back 002 (drop topic column), table still exists.
		err = migrator.Down(ctx)
		require.NoError(t, err, "Down() should roll back migration 002")

		err = pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "outbox_entries table should still exist after rolling back 002")

		// Third Down() rolls back 001 (drop table).
		err = migrator.Down(ctx)
		require.NoError(t, err, "Down() should roll back migration 001")

		err = pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, err)
		assert.False(t, exists, "outbox_entries table should not exist after rolling back 001")

		// Status should show all migrations as unapplied.
		statuses, err := migrator.Status(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, statuses)
		assert.False(t, statuses[0].Applied, "migration 001 should be unapplied")
	})
}

// ---------------------------------------------------------------------------
// T22: TestIntegration_OutboxWriter
// ---------------------------------------------------------------------------

// TestIntegration_OutboxWriter tests writing outbox entries inside and outside
// a transaction context.
func TestIntegration_OutboxWriter(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply migrations so the outbox_entries table exists.
	migrator, mErr := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, mErr, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	txm := NewTxManager(pool)
	writer := NewOutboxWriter()

	t.Run("write_in_tx", func(t *testing.T) {
		entryID := uuid.New().String()
		entry := outbox.Entry{
			ID:            entryID,
			AggregateID:   "agg-1",
			AggregateType: "test_aggregate",
			EventType:     "test.created",
			Payload:       []byte(`{"key":"value"}`),
			Metadata:      map[string]string{"trace_id": "abc-123"},
			CreatedAt:     time.Now(),
		}

		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			return writer.Write(txCtx, entry)
		})
		require.NoError(t, err, "writing outbox entry inside a tx should succeed")

		// Verify the entry was persisted.
		var aggID, eventType, status string
		err = pool.DB().QueryRow(ctx,
			"SELECT aggregate_id, event_type, status FROM outbox_entries WHERE id = $1",
			entryID,
		).Scan(&aggID, &eventType, &status)
		require.NoError(t, err, "outbox entry should be queryable after commit")
		assert.Equal(t, "agg-1", aggID)
		assert.Equal(t, "test.created", eventType)
		assert.Equal(t, "pending", status, "new outbox entry should have status='pending'")
	})

	t.Run("write_without_tx_returns_error", func(t *testing.T) {
		entry := outbox.Entry{
			ID:            uuid.New().String(),
			AggregateID:   "agg-2",
			AggregateType: "test_aggregate",
			EventType:     "test.created",
			Payload:       []byte(`{}`),
		}

		err := writer.Write(ctx, entry)
		require.Error(t, err, "writing outbox entry without a tx should fail")

		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error should be an errcode.Error")
		assert.Equal(t, ErrAdapterPGNoTx, ec.Code,
			fmt.Sprintf("error code should be %s", ErrAdapterPGNoTx))
	})

	t.Run("write_rolled_back_in_failed_tx", func(t *testing.T) {
		entryID := uuid.New().String()
		entry := outbox.Entry{
			ID:            entryID,
			AggregateID:   "agg-3",
			AggregateType: "test_aggregate",
			EventType:     "test.failed",
			Payload:       []byte(`{}`),
		}

		err := txm.RunInTx(ctx, func(txCtx context.Context) error {
			if writeErr := writer.Write(txCtx, entry); writeErr != nil {
				return writeErr
			}
			return errors.New("simulated business error")
		})
		require.Error(t, err)

		// The outbox entry should NOT exist because the tx was rolled back.
		var count int
		err = pool.DB().QueryRow(ctx,
			"SELECT count(*) FROM outbox_entries WHERE id = $1", entryID,
		).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "outbox entry should not persist after tx rollback")
	})
}

// Target: adapters/postgres coverage >= 80%
