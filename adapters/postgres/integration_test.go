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
	"github.com/ghbvf/gocell/tests/testutil"
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
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
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
		// Down() rolls back one migration at a time. With 6 migrations applied,
		// call Down() 6 times to fully revert. The outbox_entries table disappears
		// after rolling back 001 (the last iteration).
		for i := 6; i > 1; i-- {
			err := migrator.Down(ctx)
			require.NoError(t, err, "Down() should roll back migration %d without error", i)

			var exists bool
			err = pool.DB().QueryRow(ctx,
				"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
				Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "outbox_entries table should still exist after rolling back migration %d", i)
		}

		// Final Down() rolls back 001 (drops outbox_entries table).
		err := migrator.Down(ctx)
		require.NoError(t, err, "Down() should roll back migration 001")

		var exists bool
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

// ---------------------------------------------------------------------------
// B-1: TestMigrator_Applies004_WithConcurrentlyIndexes
// ---------------------------------------------------------------------------

// TestMigrator_Applies004_WithConcurrentlyIndexes verifies that migration 004
// (config_entries + config_versions) is applied correctly and that both tables
// and their indexes exist. Also verifies that running migrator.Up() twice is
// idempotent (no duplicate-table error).
// ref: pressly/goose -- +goose no transaction + CREATE INDEX CONCURRENTLY.
func TestMigrator_Applies004_WithConcurrentlyIndexes(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_004")
	require.NoError(t, err)

	// First Up: applies all 5 migrations including 004.
	require.NoError(t, migrator.Up(ctx), "first Up() must succeed")

	// Verify config_entries table exists.
	var configEntriesExists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'config_entries')").
		Scan(&configEntriesExists)
	require.NoError(t, err)
	assert.True(t, configEntriesExists, "config_entries table must exist after migration 004")

	// Verify config_versions table exists.
	var configVersionsExists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'config_versions')").
		Scan(&configVersionsExists)
	require.NoError(t, err)
	assert.True(t, configVersionsExists, "config_versions table must exist after migration 004")

	// Verify keyset index on config_entries.
	var keyIdxExists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_config_entries_key_id')").
		Scan(&keyIdxExists)
	require.NoError(t, err)
	assert.True(t, keyIdxExists, "idx_config_entries_key_id must exist after migration 004")

	// Verify version index on config_versions.
	var verIdxExists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_config_versions_config_version')").
		Scan(&verIdxExists)
	require.NoError(t, err)
	assert.True(t, verIdxExists, "idx_config_versions_config_version must exist after migration 004")

	// Idempotent: second Up() must be a no-op.
	require.NoError(t, migrator.Up(ctx), "second Up() must be idempotent (no error)")
}

// TestMigration004_StructuralAssertions verifies the column layout of
// config_entries and config_versions after migration 004 is applied
// (F-D-3 / RL-MIG-01 evidence). Also asserts idx_outbox_pending_v2 existence
// (introduced by migration 005).
func TestMigration004_StructuralAssertions(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_struct")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")

	// --- config_entries columns ---
	wantConfigEntryColumns := []string{"id", "key", "value", "sensitive", "version", "created_at", "updated_at"}
	for _, col := range wantConfigEntryColumns {
		col := col
		t.Run("config_entries_has_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				`SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'config_entries' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "config_entries must have column %q", col)
		})
	}

	// --- config_versions columns ---
	wantConfigVersionColumns := []string{"id", "config_id", "version", "value", "sensitive", "published_at"}
	for _, col := range wantConfigVersionColumns {
		col := col
		t.Run("config_versions_has_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				`SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'config_versions' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "config_versions must have column %q", col)
		})
	}

	// --- RL-MIG-01: idx_outbox_pending_v2 (migration 005) ---
	t.Run("idx_outbox_pending_v2_exists", func(t *testing.T) {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_outbox_pending_v2')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "idx_outbox_pending_v2 must exist (RL-MIG-01 evidence, migration 005)")
	})
}

// ---------------------------------------------------------------------------
// T7: TestMigration006_ConfigVersionsConfigIDIndex
// ---------------------------------------------------------------------------

// TestMigration006_ConfigVersionsConfigIDIndex verifies that migration 006
// creates idx_config_versions_config_id and that an eq-lookup on config_id
// uses an Index Scan (not a Seq Scan).
func TestMigration006_ConfigVersionsConfigIDIndex(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_006")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations including 006")

	// Verify idx_config_versions_config_id exists.
	var idxExists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_config_versions_config_id')").
		Scan(&idxExists)
	require.NoError(t, err)
	assert.True(t, idxExists, "idx_config_versions_config_id must exist after migration 006")

	// Disable seq scan to force index usage in EXPLAIN, then verify plan.
	// This confirms the planner can use the new index for eq-lookup on config_id.
	_, err = pool.DB().Exec(ctx, "SET enable_seqscan = off")
	require.NoError(t, err)

	rows, err := pool.DB().Query(ctx,
		"EXPLAIN (FORMAT JSON) SELECT * FROM config_versions WHERE config_id = 'test-id'")
	require.NoError(t, err)
	defer rows.Close()

	var planJSON string
	require.True(t, rows.Next(), "EXPLAIN should return at least one row")
	require.NoError(t, rows.Scan(&planJSON))
	require.NoError(t, rows.Err())

	assert.Contains(t, planJSON, "idx_config_versions_config_id",
		"query plan should reference idx_config_versions_config_id when seq scan disabled")

	// Re-enable seq scan to not affect other tests (belt-and-suspenders;
	// the connection returns to pool and settings reset on next acquire).
	_, _ = pool.DB().Exec(ctx, "SET enable_seqscan = on")
}

// Target: adapters/postgres coverage >= 80%
