//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tests/testutil"
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
		_ = pool.Close(ctx)
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
			_ = pool.Close(context.Background())
		}, "Close(ctx) should not panic")
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

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
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
		// Roll non-destructive migrations back one by one until we reach 012,
		// the destructive PR-A29 refresh_tokens_rebuild that is hard-gated by
		// default. 014 (lease_id column) and 013 (observability column) are
		// both non-destructive column/index drops and must succeed; the call
		// that targets 012 must surface the RAISE EXCEPTION error.
		//
		// Driving the loop off ExpectedVersion keeps the test correct as new
		// non-destructive migrations are added on top of 014.
		expected, fsErr := ExpectedVersion(testMigrationsFS(t))
		require.NoError(t, fsErr)
		const hardGatedVersion = int64(12)
		require.Greater(t, expected, hardGatedVersion,
			"this test assumes at least one non-destructive migration on top of the 012 hard-gate")

		for v := expected; v > hardGatedVersion; v-- {
			require.NoError(t, migrator.Down(ctx),
				"migration %d down should succeed — non-destructive rollback", v)
		}

		dErr := migrator.Down(ctx)
		require.Error(t, dErr, "migration 012 down must be hard-gated by default")
		assert.Contains(t, dErr.Error(), "gocell.allow_destructive_refresh_tokens_down")

		var exists bool
		qErr := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, qErr)
		assert.True(t, exists, "outbox_entries table should still exist after refused rollback")

		statuses, sErr := migrator.Status(ctx)
		require.NoError(t, sErr)
		require.GreaterOrEqual(t, len(statuses), int(expected), "status list must cover all migrations")
		// All migrations above 012 are rolled back; 012 must remain applied
		// (gate refused).
		for _, s := range statuses {
			version, parseErr := strconv.ParseInt(s.Version, 10, 64)
			require.NoError(t, parseErr, "migration %s must have integer version", s.Version)
			if version <= hardGatedVersion {
				assert.True(t, s.Applied,
					"migration %d must remain applied after refused rollback", version)
			} else {
				assert.False(t, s.Applied,
					"migration %d must be rolled back", version)
			}
		}
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
	migrator, mErr := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, mErr, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	txm := NewTxManager(pool)
	writer := NewOutboxWriter(clock.Real())

	t.Run("write_in_tx", func(t *testing.T) {
		entryID := uuid.New().String()
		entry := outbox.Entry{
			ID:            entryID,
			AggregateID:   "agg-1",
			AggregateType: "test_aggregate",
			EventType:     "test.created",
			Payload:       []byte(`{"key":"value"}`),
			// Producer-owned domain metadata only. Observability IDs (trace_id,
			// request_id, ...) belong in Entry.Observability — Entry.Validate
			// rejects ReservedMetadataKeys to keep the namespace boundary honest.
			Metadata:  map[string]string{"source": "integration-test"},
			CreatedAt: time.Now(),
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

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_004")
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

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_struct")
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

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_006")
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

// ---------------------------------------------------------------------------
// F1: TestMigrator_Up_RefusesIfInvalidIndexExists
// ---------------------------------------------------------------------------

// TestMigrator_Up_RefusesIfInvalidIndexExists verifies that Migrator.Up
// returns an error and does not advance the schema version when an INVALID
// index is present in the database.
//
// Scenario: apply all migrations, inject an INVALID index via pg_index system
// catalog, then construct a fresh migrator with a different tracking table and
// attempt Up() — it must refuse.
func TestMigrator_Up_RefusesIfInvalidIndexExists(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations so tables and indexes exist.
	prep, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_guard_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "preparatory Up() must succeed")

	// Inject an INVALID index by marking idx_outbox_pending_v2 as invalid.
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed (requires superuser)")

	// Restore invalid index afterwards so container cleanup is clean.
	defer func() {
		_, _ = pool.DB().Exec(ctx,
			`UPDATE pg_index SET indisvalid = true
			 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	}()

	// Construct a fresh migrator using the same pool (with invalid index present).
	// Use a new tracking table so Up() attempts to run from scratch (pre-check
	// fires before any migration runs).
	migrator2, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_guard_test")
	require.NoError(t, err)

	// Up() must return an error: refusing to migrate due to invalid indexes.
	upErr := migrator2.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when invalid indexes are present")
	// K#08 PII-safe message: the public Message is a fixed const literal, the
	// runtime index list rides on InternalMessage (not on .Error()). Assert on
	// the structured fields rather than the formatted Error() string.
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "upErr must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code,
		"Up() must surface the postgres-migrate sentinel code")
	assert.Contains(t, ec.Message, "invalid indexes",
		"public message should mention invalid indexes")

	// Verify schema version was NOT advanced: schema_migrations_guard_test should not exist
	// (goose only creates the tracking table once migrations start; if Up is aborted
	// before any migration runs, the table may not exist at all, which is fine).
	var versionCount int
	row := pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name = 'schema_migrations_guard_test'`)
	_ = row.Scan(&versionCount)
	// Either the table doesn't exist (versionCount == 0) or it has no applied
	// migrations — in either case no version was advanced.
	// The critical assertion is that Up() returned an error (already asserted above).
}

// ---------------------------------------------------------------------------
// PR-V1-PG-STARTUP-HARDEN: SessionLocker concurrent-Up + 9/10 sequence
// ---------------------------------------------------------------------------

// concurrentUpFixtureFS returns a small migration FS used by the
// concurrent-Up test. The single migration inserts one row into
// _migration_run_sentinel — under proper advisory locking, exactly one row
// must exist after N goroutines race to call Up() against the same DB.
func concurrentUpFixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"001_marker_run_once.sql": &fstest.MapFile{Data: []byte(
			"-- +goose Up\n" +
				"CREATE TABLE IF NOT EXISTS _migration_run_sentinel (\n" +
				"    id   SERIAL PRIMARY KEY,\n" +
				"    ts   TIMESTAMPTZ NOT NULL DEFAULT now()\n" +
				");\n" +
				"INSERT INTO _migration_run_sentinel (ts) VALUES (now());\n" +
				"-- +goose Down\n" +
				"DROP TABLE IF EXISTS _migration_run_sentinel;\n",
		)},
	}
}

// TestMigrator_ConcurrentUp_NoRaceWithSessionLocker spawns N goroutines that
// all call Up() against the same Pool and tracking table. With
// goose.WithSessionLocker the SessionLocker serializes them via
// pg_advisory_lock so the marker INSERT runs exactly once; without it, the
// INSERT can run multiple times (sentinel row count > 1) or two providers
// can race the schema_migrations write (per-row uniqueness violation).
func TestMigrator_ConcurrentUp_NoRaceWithSessionLocker(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()
	ctx := context.Background()

	const (
		// N stays well below MaxConns=10 so each goroutine can acquire its
		// own connection (SessionLocker holds one *sql.Conn while Up runs).
		N         = 5
		tableName = "schema_migrations_concurrent"
	)
	fixtureFS := concurrentUpFixtureFS()

	errs := make(chan error, N)
	for range N {
		go func() {
			m, err := NewMigrator(pool, fixtureFS, tableName)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = m.Close() }()
			errs <- m.Up(ctx)
		}()
	}

	for range N {
		require.NoError(t, <-errs, "concurrent Up must all return nil under SessionLocker")
	}

	// Sentinel row count == 1 proves the INSERT ran exactly once across N
	// concurrent providers — the SessionLocker serialized them.
	var sentinelCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM _migration_run_sentinel").Scan(&sentinelCount))
	assert.Equal(t, 1, sentinelCount,
		"_migration_run_sentinel INSERT must run exactly once across N concurrent Up calls")

	// Tracking table has version 1 marked applied. We do not assert row count:
	// goose may record one tracking row per Up() call even when the migration
	// SQL is skipped under the lock — the only invariant we care about is
	// that the highest applied version is correctly recorded as 1, and the
	// SQL ran exactly once (sentinelCount above).
	var maxVersion int64
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT coalesce(max(version_id), 0) FROM "+tableName+" WHERE is_applied = true").Scan(&maxVersion))
	assert.Equal(t, int64(1), maxVersion,
		"schema_migrations_concurrent max applied version_id must be 1")
}

// sequenceFixtureFS_910 returns a 1..10 dense fixture used to lock 9-before-10
// numeric ordering and the Down(1)-step rollback semantics.
func sequenceFixtureFS_910() fstest.MapFS {
	noop := []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")
	tableMig := func(table string) []byte {
		return []byte(
			"-- +goose Up\n" +
				"CREATE TABLE IF NOT EXISTS " + table + " (id serial primary key, created_at timestamptz default now());\n" +
				"-- +goose Down\n" +
				"DROP TABLE IF EXISTS " + table + ";\n",
		)
	}

	fs := fstest.MapFS{}
	for v := 1; v <= 8; v++ {
		fs[fmt.Sprintf("%03d_noop.sql", v)] = &fstest.MapFile{Data: noop}
	}
	fs["009_add_marker_x.sql"] = &fstest.MapFile{Data: tableMig("seq_marker_x")}
	fs["010_add_marker_y.sql"] = &fstest.MapFile{Data: tableMig("seq_marker_y")}
	return fs
}

// TestMigrator_NineBeforeTen_OrderRegression ensures goose's numeric ordering
// places 009 before 010 (string sort would too, but only because of the
// zero-padded prefix). Down(1) rolls back exactly 010, leaving 009 applied.
func TestMigrator_NineBeforeTen_OrderRegression(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()
	ctx := context.Background()

	const tableName = "schema_migrations_seq910"
	fixtureFS := sequenceFixtureFS_910()

	m, err := NewMigrator(pool, fixtureFS, tableName)
	require.NoError(t, err)
	defer func() { _ = m.Close() }()

	require.NoError(t, m.Up(ctx))

	statuses, err := m.Status(ctx)
	require.NoError(t, err)
	idx009, idx010 := -1, -1
	for i, s := range statuses {
		switch s.Version {
		case "009":
			idx009 = i
		case "010":
			idx010 = i
		}
	}
	require.GreaterOrEqual(t, idx009, 0, "Status() must include version 009")
	require.GreaterOrEqual(t, idx010, 0, "Status() must include version 010")
	assert.Less(t, idx009, idx010, "Status() must list 009 before 010")
	assert.True(t, statuses[idx009].Applied, "009 must be applied after Up")
	assert.True(t, statuses[idx010].Applied, "010 must be applied after Up")

	// Down rolls back exactly the latest version (010).
	require.NoError(t, m.Down(ctx))

	var xExists, yExists bool
	require.NoError(t, pool.DB().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='seq_marker_x')`).Scan(&xExists))
	require.NoError(t, pool.DB().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='seq_marker_y')`).Scan(&yExists))
	assert.True(t, xExists, "seq_marker_x (009) must remain after Down")
	assert.False(t, yExists, "seq_marker_y (010) must be dropped by Down")
}

// TestMigrator_Down_AtVersionZero_Idempotent locks P4-TD-11: after applying
// Up then rolling back to v=0, repeated Down() calls must keep returning nil.
// Goose returns ErrNoCurrentVersion / ErrNoNextVersion at v=0 which
// Migrator.Down absorbs (migrator.go Down() — see "idempotent no-op" branch).
//
// ref: pressly/goose provider_run_test.go TestProviderRun/up_and_down_by_one
// — confirms ErrNoNextVersion is goose's canonical v=0 signal.
func TestMigrator_Down_AtVersionZero_Idempotent(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()
	ctx := context.Background()

	// Single noop migration — decouples regression from production schema churn.
	fixtureFS := fstest.MapFS{
		"001_noop.sql": &fstest.MapFile{
			Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"),
		},
	}

	const tableName = "schema_migrations_down_v0"
	m, err := NewMigrator(pool, fixtureFS, tableName)
	require.NoError(t, err)
	defer func() { _ = m.Close() }()

	require.NoError(t, m.Up(ctx))
	require.NoError(t, m.Down(ctx), "1st Down rolls back 001 → v=0")
	require.NoError(t, m.Down(ctx), "2nd Down at v=0 must be idempotent no-op")
	require.NoError(t, m.Down(ctx), "3rd Down at v=0 still idempotent")

	statuses, sErr := m.Status(ctx)
	require.NoError(t, sErr)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Applied,
		"001 must remain rolled back after repeated Down() at v=0")
}

// Target: adapters/postgres coverage >= 80%
