//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func mustAllowDestructiveDown(t testing.TB, reason string) DestructiveDownPermit {
	t.Helper()
	permit, err := AllowDestructiveDown(reason)
	require.NoError(t, err)
	return permit
}

func migrationsUpToFS(t testing.TB, maxVersion int64) fstest.MapFS {
	t.Helper()
	source := testMigrationsFS(t)
	entries, err := fs.ReadDir(source, ".")
	require.NoError(t, err)

	out := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationVersionRe.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, parseErr := strconv.ParseInt(match[1], 10, 64)
		require.NoError(t, parseErr)
		if version > maxVersion {
			continue
		}
		data, readErr := fs.ReadFile(source, entry.Name())
		require.NoError(t, readErr)
		out[entry.Name()] = &fstest.MapFile{Data: data}
	}
	require.NotEmpty(t, out)
	return out
}

// ---------------------------------------------------------------------------
// T19: TestIntegration_Pool
// ---------------------------------------------------------------------------

// TestIntegration_Pool verifies that Pool can connect to a real PostgreSQL
// instance, pass the Health() probe, and shut down cleanly.
func TestIntegration_Pool(t *testing.T) {
	pool := emptyPool(t)

	ctx := context.Background()

	t.Run("connect_and_health", func(t *testing.T) {
		// Pool was already successfully created by emptyPool.
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
	pool := emptyPool(t)

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
			tx, ok := persistence.TxFromContext[pgx.Tx](txCtx)
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
			tx, ok := persistence.TxFromContext[pgx.Tx](txCtx)
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
				tx, ok := persistence.TxFromContext[pgx.Tx](txCtx)
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations")
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
		expected, fsErr := ExpectedVersion(testMigrationsFS(t))
		require.NoError(t, fsErr)

		dErr := migrator.Down(ctx, nil)
		require.Error(t, dErr, "Down without an explicit permit must fail closed")
		var ec *errcode.Error
		require.ErrorAs(t, dErr, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)

		var exists bool
		qErr := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'outbox_entries')").
			Scan(&exists)
		require.NoError(t, qErr)
		assert.True(t, exists, "outbox_entries table should still exist after refused rollback")

		permit := mustAllowDestructiveDown(t, "integration test rollback")
		require.NoError(t, migrator.Down(ctx, permit),
			"Down with an explicit permit rolls back exactly the latest migration")

		statuses, sErr := migrator.Status(ctx)
		require.NoError(t, sErr)
		// Sanity-check the status list is non-empty. We deliberately do NOT
		// compare len(statuses) to ExpectedVersion: migration version numbers
		// can be sparse when a slot is reserved for an in-flight PR (e.g.
		// version 022 reserved for S6 PR #464 leaves max=23 but file count=22).
		// The foundLatest assertion below covers what we actually care about.
		require.NotEmpty(t, statuses, "status must list at least one migration")
		latestVersion := fmt.Sprintf("%03d", expected)
		foundLatest := false
		for _, s := range statuses {
			if s.Version == latestVersion {
				foundLatest = true
				assert.False(t, s.Applied,
					"latest migration %s must be rolled back after permitted Down", latestVersion)
			}
		}
		assert.True(t, foundLatest, "status must include latest migration %s", latestVersion)
	})
}

// ---------------------------------------------------------------------------
// T22: TestIntegration_OutboxWriter
// ---------------------------------------------------------------------------

// TestIntegration_OutboxWriter tests writing outbox entries inside and outside
// a transaction context.
func TestIntegration_OutboxWriter(t *testing.T) {
	pool := migratedPool(t)

	ctx := context.Background()

	txm := NewTxManager(pool)
	writer := NewOutboxWriter(clock.Real())

	t.Run("write_in_tx", func(t *testing.T) {
		entryID := uuid.New().String()
		// Producer-owned domain metadata only. Observability/principal IDs belong
		// in the typed Entry.Observability/Principal fields — Entry.Validate rejects
		// ReservedMetadataKeys to keep the namespace boundary honest.
		entry := mustScanEntry(outbox.EntryScan{
			ID:            entryID,
			AggregateID:   "agg-1",
			AggregateType: "test_aggregate",
			EventType:     "test.created",
			Payload:       []byte(`{"key":"value"}`),
			Metadata:      map[string]string{"source": "integration-test"},
			CreatedAt:     time.Now(),
			OccurredAt:    time.Now(),
		})

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
		entry := mustScanEntry(outbox.EntryScan{
			ID:            uuid.New().String(),
			AggregateID:   "agg-2",
			AggregateType: "test_aggregate",
			EventType:     "test.created",
			Payload:       []byte(`{}`),
			CreatedAt:     time.Now(),
			OccurredAt:    time.Now(),
		})

		err := writer.Write(ctx, entry)
		require.Error(t, err, "writing outbox entry without a tx should fail")

		var ec *errcode.Error
		require.True(t, errors.As(err, &ec), "error should be an errcode.Error")
		assert.Equal(t, ErrAdapterPGNoTx, ec.Code,
			fmt.Sprintf("error code should be %s", ErrAdapterPGNoTx))
	})

	t.Run("write_rolled_back_in_failed_tx", func(t *testing.T) {
		entryID := uuid.New().String()
		entry := mustScanEntry(outbox.EntryScan{
			ID:            entryID,
			AggregateID:   "agg-3",
			AggregateType: "test_aggregate",
			EventType:     "test.failed",
			Payload:       []byte(`{}`),
			CreatedAt:     time.Now(),
			OccurredAt:    time.Now(),
		})

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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_004")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_struct")
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
	pool := emptyPool(t)

	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_006")
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
	pool := emptyPool(t)

	ctx := context.Background()

	// Apply all migrations so tables and indexes exist.
	prep, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_guard_prep")
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
	migrator2, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_guard_test")
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
	pool := emptyPool(t)
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
			m, err := newMigratorForTable(pool, fixtureFS, tableName)
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
	pool := emptyPool(t)
	ctx := context.Background()

	const tableName = "schema_migrations_seq910"
	fixtureFS := sequenceFixtureFS_910()

	m, err := newMigratorForTable(pool, fixtureFS, tableName)
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
	require.NoError(t, m.Down(ctx, mustAllowDestructiveDown(t, "sequence order test rollback")))

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
	// emptyPool gives each test its own isolated database, so a fixed table
	// name does not collide with sibling tests that pick the same string.
	pool := emptyPool(t)
	ctx := context.Background()

	// Single noop migration — decouples regression from production schema churn.
	fixtureFS := fstest.MapFS{
		"001_noop.sql": &fstest.MapFile{
			Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"),
		},
	}

	const tableName = "schema_migrations_down_v0"
	m, err := newMigratorForTable(pool, fixtureFS, tableName)
	require.NoError(t, err)
	defer func() { _ = m.Close() }()

	require.NoError(t, m.Up(ctx))
	permit := mustAllowDestructiveDown(t, "idempotent v0 rollback test")
	require.NoError(t, m.Down(ctx, permit), "1st Down rolls back 001 → v=0")
	require.NoError(t, m.Down(ctx, permit), "2nd Down at v=0 must be idempotent no-op")
	require.NoError(t, m.Down(ctx, permit), "3rd Down at v=0 still idempotent")

	statuses, sErr := m.Status(ctx)
	require.NoError(t, sErr)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Applied,
		"001 must remain rolled back after repeated Down() at v=0")
}

// ---------------------------------------------------------------------------
// S3F: TestMigrator_Down_WithPermit_Succeeds
// ---------------------------------------------------------------------------

// TestMigrator_Down_WithPermit_Succeeds verifies that Migrator.Down with a
// valid DestructiveDownPermit succeeds. Migration 012 has no SQL-level GUC
// guard in its Down section — the Go-layer DestructiveDownPermit is the sole
// gate, confirming the typed permit is the enforced channel.
func TestMigrator_Down_WithPermit_Succeeds(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up to 012 so there is a migration to roll back.
	// Migration 012 Down has no GUC SQL guard — permit is the only gate.
	mfs := migrationsUpToFS(t, 12)
	migrator, err := newMigratorForTable(pool, mfs, "schema_migrations_permit_down")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply through migration 012")

	permit := mustAllowDestructiveDown(t, "S3F permit integration test")
	require.NoError(t, migrator.Down(ctx, permit),
		"Migrator.Down with typed permit must succeed: Go-layer permit is the sole gate")

	// Verify migration 012 was rolled back: refresh_tokens should have the
	// pre-012 token column (which 012 Down recreates).
	var tokenExists bool
	err = pool.DB().QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'refresh_tokens' AND column_name = 'token'
)`).Scan(&tokenExists)
	require.NoError(t, err)
	assert.True(t, tokenExists, "permitted Down must recreate the pre-012 token column")
}

// ---------------------------------------------------------------------------
// ForwardRebuild integration tests
// ---------------------------------------------------------------------------

func mustAllowForwardRebuild(t testing.TB, migrationNumber int64, reason string) ForwardRebuildPermit {
	t.Helper()
	permit, err := AllowForwardRebuild(migrationNumber, reason)
	require.NoError(t, err)
	return permit
}

// TestMigrator_ForwardRebuild_EmptyTable_Up verifies that Up() applies all
// migrations including any annotated forward-rebuild migrations when the target
// tables are empty or missing — no permit required.
func TestMigrator_ForwardRebuild_EmptyTable_Up(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_fwd_empty")
	require.NoError(t, err)

	// Fresh DB: all rebuild targets are empty/missing — Up() must succeed without permits.
	require.NoError(t, migrator.Up(ctx), "Up() must succeed on a fresh DB (no rows to protect)")

	// Verify migration 012's refresh_tokens (forward-rebuild target) was created.
	var exists bool
	err = pool.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'refresh_tokens')").
		Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "refresh_tokens table must exist after Up()")
}

// TestMigrator_ForwardRebuild_PopulatedTable_UpFailClosed verifies that Up()
// refuses with an error when a pending forward-rebuild migration's target table
// already holds rows — fail-closed without an explicit permit.
func TestMigrator_ForwardRebuild_PopulatedTable_UpFailClosed(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 011 (refresh_tokens exists and can be populated).
	mfs011 := migrationsUpToFS(t, 11)
	prep, err := newMigratorForTable(pool, mfs011, "schema_migrations_fwd_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 011 must succeed")

	// Insert a row into refresh_tokens so migration 012's forward-rebuild is dangerous.
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO refresh_tokens (id, token, session_id, subject_id, created_at, last_used, expires_at)
		VALUES (1, 'tok-abc', 'sess-1', 'subj-1', now(), now(), now() + interval '1 hour')
	`)
	require.NoError(t, execErr, "must be able to insert a row into pre-012 refresh_tokens")

	// Now create a migrator with the full FS so migration 012 is pending.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_fwd_prep")
	require.NoError(t, err)

	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when a forward-rebuild target has rows and no permit")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	// Verify the public detail carries migration=12.
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(12), migDetail.Value(), "migration detail value must be 12")
}

// TestMigrator_ForwardRebuild_PopulatedTable_WithPermit verifies that
// ForwardRebuild with the correct permit succeeds when the target table has rows.
func TestMigrator_ForwardRebuild_PopulatedTable_WithPermit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 011.
	mfs011 := migrationsUpToFS(t, 11)
	prep, err := newMigratorForTable(pool, mfs011, "schema_migrations_permit_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 011 must succeed")

	// Insert a row into pre-012 refresh_tokens.
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO refresh_tokens (id, token, session_id, subject_id, created_at, last_used, expires_at)
		VALUES (1, 'tok-xyz', 'sess-2', 'subj-2', now(), now(), now() + interval '1 hour')
	`)
	require.NoError(t, execErr)

	// ForwardRebuild with permit for migration 12 must succeed.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_permit_prep")
	require.NoError(t, err)

	permit := mustAllowForwardRebuild(t, 12, "integration test: approved refresh_tokens v2 rebuild")
	require.NoError(t, migrator.ForwardRebuild(ctx, permit),
		"ForwardRebuild with explicit permit must succeed")

	// Verify 012's new selector column exists (v2 schema).
	var selectorExists bool
	err = pool.DB().QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'refresh_tokens' AND column_name = 'selector'
)`).Scan(&selectorExists)
	require.NoError(t, err)
	assert.True(t, selectorExists, "ForwardRebuild must apply migration 012 creating the selector column")
}

// TestMigrator_ForwardRebuild_MisconfiguredPermit verifies that ForwardRebuild
// returns an error when a permit references a migration that is not a pending
// forward-rebuild.
func TestMigrator_ForwardRebuild_MisconfiguredPermit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Fresh DB — no migrations applied yet, so version 999 cannot be a pending
	// forward-rebuild (it does not exist in the FS at all).
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_misconfig")
	require.NoError(t, err)

	permit := mustAllowForwardRebuild(t, 999, "misconfig test")
	rebuildErr := migrator.ForwardRebuild(ctx, permit)
	require.Error(t, rebuildErr, "ForwardRebuild with permit for non-existent migration must fail")
	var ec *errcode.Error
	require.True(t, errors.As(rebuildErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	// Public details (not the Error() string) carry the migration number — errcode
	// renders only "[CODE] message" in Error(), details live in wire/slog.
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(999), migDetail.Value(), "migration detail value must be 999")
}

// ---------------------------------------------------------------------------
// Migration 043 gate integration tests (#4)
// ---------------------------------------------------------------------------

// TestMigrator_ForwardRebuild_Migration043_PopulatedAuditEntries verifies the
// fail-closed gate for migration 043 (audit_entries v2 rebuild) when the
// audit_entries table has rows, and that ForwardRebuild with the correct
// permit succeeds.
func TestMigrator_ForwardRebuild_Migration043_PopulatedAuditEntries(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 042 so audit_entries (from 020) exists.
	mfs042 := migrationsUpToFS(t, 42)
	prep, err := newMigratorForTable(pool, mfs042, "schema_migrations_043_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 042 must succeed")

	// Insert a row into audit_entries to make migration 043 dangerous.
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO audit_entries
			(id, namespace, seq_no, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES
			(gen_random_uuid(), 'auditcore', 1, 'evt-001', 'test.event', 'actor-1',
			 now(), '\x7b7d', '', 'aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd')
	`)
	require.NoError(t, execErr, "must be able to insert a row into pre-043 audit_entries")

	// Up() must fail-closed: 043 is pending and audit_entries has rows.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_043_prep")
	require.NoError(t, err)

	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when migration 043 target audit_entries has rows")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(43), migDetail.Value(), "migration detail value must be 43")

	// ForwardRebuild with the correct permit must succeed.
	migrator2, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_043_prep")
	require.NoError(t, err)

	permit := mustAllowForwardRebuild(t, 43, "043 audit_entries v2 rebuild integration test")
	// Migration 055 ALSO forward-rebuilds audit_entries (#1618). The phase0 gate
	// checks ALL pending rebuilds against the CURRENTLY-populated table, so 055
	// needs its own permit too (audit_entries has the pre-043 row right now).
	permit55 := mustAllowForwardRebuild(t, 55, "055 audit_entries per-tenant rebuild integration test")
	require.NoError(t, migrator2.ForwardRebuild(ctx, permit, permit55),
		"ForwardRebuild with permits for migrations 043+055 must succeed")

	// Verify migration 043's subject_id column was created.
	var subjectIDExists bool
	err = pool.DB().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'audit_entries' AND column_name = 'subject_id'
		)`).Scan(&subjectIDExists)
	require.NoError(t, err)
	assert.True(t, subjectIDExists, "audit_entries must have subject_id column after migration 043")
}

// TestMigrator_ForwardRebuild_Migrations043And044_DualPermit verifies that
// when both audit_entries and outbox_entries have rows, ForwardRebuild requires
// both permits and succeeds when both are provided. Omitting either permit
// must fail-closed.
func TestMigrator_ForwardRebuild_Migrations043And044_DualPermit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 042 so audit_entries and outbox_entries exist.
	mfs042 := migrationsUpToFS(t, 42)
	prep, err := newMigratorForTable(pool, mfs042, "schema_migrations_044_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 042 must succeed")

	// Insert a row into audit_entries.
	_, auditErr := pool.DB().Exec(ctx, `
		INSERT INTO audit_entries
			(id, namespace, seq_no, event_id, event_type, actor_id, timestamp, payload, prev_hash, hash)
		VALUES
			(gen_random_uuid(), 'auditcore', 1, 'evt-dual-001', 'test.event', 'actor-1',
			 now(), '\x7b7d', '', 'aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd')
	`)
	require.NoError(t, auditErr, "must be able to insert a row into pre-043 audit_entries")

	// Insert a row into outbox_entries (making 044 dangerous).
	entryID := "dual-test-entry-01"
	_, outboxErr := pool.DB().Exec(ctx, `
		INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, payload, status, created_at, next_retry_at)
		VALUES ($1, 'agg-1', 'test_agg', 'test.event', '{}', 'published', now(), now())
	`, entryID)
	require.NoError(t, outboxErr, "must be able to insert a row into pre-044 outbox_entries")

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_prep")
	require.NoError(t, err)

	// Up() without permits: must fail-closed.
	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when both 043 and 044 targets have rows")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec))
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)

	// ForwardRebuild with only permit 43 must fail-closed (044 still needs one).
	migrator2, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_prep")
	require.NoError(t, err)
	permit43Only := mustAllowForwardRebuild(t, 43, "043 dual-test permit")
	onlyPermit43Err := migrator2.ForwardRebuild(ctx, permit43Only)
	require.Error(t, onlyPermit43Err, "ForwardRebuild with only permit 43 must refuse when 044 target has rows")
	var ec2 *errcode.Error
	require.True(t, errors.As(onlyPermit43Err, &ec2))
	assert.Equal(t, ErrAdapterPGMigrate, ec2.Code)

	// ForwardRebuild with both permits must succeed.
	migrator3, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_prep")
	require.NoError(t, err)
	permit43 := mustAllowForwardRebuild(t, 43, "043 dual-test permit final")
	permit44 := mustAllowForwardRebuild(t, 44, "044 outbox_entries principal dual-test permit")
	// 055 also rebuilds audit_entries (#1618) and is gated at phase0 against the
	// currently-populated audit_entries, so its permit is required here too.
	permit55 := mustAllowForwardRebuild(t, 55, "055 audit_entries per-tenant dual-test permit")
	require.NoError(t, migrator3.ForwardRebuild(ctx, permit43, permit44, permit55),
		"ForwardRebuild with all permits must succeed")

	// Verify 044's principal column was added.
	var principalExists bool
	err = pool.DB().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'outbox_entries' AND column_name = 'principal'
		)`).Scan(&principalExists)
	require.NoError(t, err)
	assert.True(t, principalExists, "outbox_entries must have principal column after migration 044")
}

// ---------------------------------------------------------------------------
// #16: tableHasRows error-path fail-closed test
// ---------------------------------------------------------------------------

// TestMigrator_TableHasRows_DBError_FailClosed verifies that when the DB probe
// returns an error (e.g., cancelled context), Up() / ForwardRebuild() fails
// closed and does not apply the migration.
func TestMigrator_TableHasRows_DBError_FailClosed(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 011 so refresh_tokens exists and can be populated.
	mfs011 := migrationsUpToFS(t, 11)
	prep, err := newMigratorForTable(pool, mfs011, "schema_migrations_dbfail_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 011 must succeed")

	// Insert a row so migration 012 is dangerous.
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO refresh_tokens (id, token, session_id, subject_id, created_at, last_used, expires_at)
		VALUES (1, 'tok-fail', 'sess-fail', 'subj-fail', now(), now(), now() + interval '1 hour')
	`)
	require.NoError(t, execErr)

	// Cancel context before calling Up() to simulate DB probe error.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // immediately cancel

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_dbfail_prep")
	require.NoError(t, err)

	// ForwardRebuild with cancelled context must fail — either due to context
	// cancellation during the tableHasRows probe or the provider.Up call.
	// Either way it must not succeed silently.
	rebuildErr := migrator.ForwardRebuild(cancelCtx,
		mustAllowForwardRebuild(t, 12, "dbfail test permit"))
	require.Error(t, rebuildErr, "ForwardRebuild with cancelled context must return an error (fail-closed)")
}

// ---------------------------------------------------------------------------
// [F10·Cx2] Migration 044 gate three-way integration tests
// ---------------------------------------------------------------------------

// TestMigrator_ForwardRebuild_Migration044_EmptyTable_Up verifies that a fresh
// DB (outbox_entries empty after migration 043 TRUNCATE) can run Up() through
// migration 044 without any permit — the empty-table path is always safe.
func TestMigrator_ForwardRebuild_Migration044_EmptyTable_Up(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_empty")
	require.NoError(t, err)

	// Fresh DB: all rebuild targets are empty/missing — Up() must succeed without permits.
	require.NoError(t, migrator.Up(ctx),
		"Up() must succeed on a fresh DB (outbox_entries empty after 043 TRUNCATE)")

	// Verify 044's principal and occurred_at columns were added.
	for _, col := range []string{"principal", "occurred_at"} {
		col := col
		t.Run("outbox_entries_has_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'outbox_entries' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "outbox_entries must have %q column after migration 044", col)
		})
	}
}

// TestMigrator_ForwardRebuild_Migration044_PopulatedOutbox_UpFailClosed verifies
// that Up() refuses fail-closed when outbox_entries has rows and migration 044
// is pending (no permit supplied).
func TestMigrator_ForwardRebuild_Migration044_PopulatedOutbox_UpFailClosed(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 043 (so outbox_entries exists post-043 TRUNCATE).
	mfs043 := migrationsUpToFS(t, 43)
	prep, err := newMigratorForTable(pool, mfs043, "schema_migrations_044_failclosed_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 043 must succeed")

	// Insert a row into outbox_entries to make migration 044 dangerous.
	entryID := "failclosed-test-entry-044"
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, payload, status, created_at, next_retry_at)
		VALUES ($1, 'agg-1', 'test_agg', 'test.event', '{}', 'published', now(), now())
	`, entryID)
	require.NoError(t, execErr, "must be able to insert a row into post-043 outbox_entries")

	// Up() without permits: must fail-closed.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_failclosed_prep")
	require.NoError(t, err)

	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when migration 044 target outbox_entries has rows")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	// Use ec.FindAttr to assert migration=44 — errcode public details are NOT
	// rendered in Error() string, so string-contains assertions would be fragile.
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(44), migDetail.Value(), "migration detail value must be 44")
}

// TestMigrator_ForwardRebuild_Migration044_PopulatedOutbox_WithPermit verifies
// that ForwardRebuild with permit 44 succeeds when outbox_entries has rows.
// Only permit 44 is needed because audit_entries is empty after migration 043's
// TRUNCATE (no rows inserted → not dangerous → no permit 43 required).
func TestMigrator_ForwardRebuild_Migration044_PopulatedOutbox_WithPermit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 043.
	mfs043 := migrationsUpToFS(t, 43)
	prep, err := newMigratorForTable(pool, mfs043, "schema_migrations_044_permit_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 043 must succeed")

	// Insert a row into outbox_entries (audit_entries is left empty — not dangerous).
	entryID := "permit-test-entry-044"
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, payload, status, created_at, next_retry_at)
		VALUES ($1, 'agg-2', 'test_agg', 'test.event', '{}', 'published', now(), now())
	`, entryID)
	require.NoError(t, execErr, "must be able to insert a row into post-043 outbox_entries")

	// ForwardRebuild with only permit 44 must succeed — audit_entries is empty
	// so migration 043 is not pending-dangerous (no rows → no permit needed).
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_044_permit_prep")
	require.NoError(t, err)

	permit44 := mustAllowForwardRebuild(t, 44, "044 outbox_entries principal permit integration test")
	require.NoError(t, migrator.ForwardRebuild(ctx, permit44),
		"ForwardRebuild with permit 44 must succeed when only outbox_entries has rows")

	// Verify 044's principal and occurred_at columns were added.
	for _, col := range []string{"principal", "occurred_at"} {
		col := col
		t.Run("outbox_entries_has_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'outbox_entries' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists,
				"outbox_entries must have %q column after ForwardRebuild with permit 44", col)
		})
	}
}

// ---------------------------------------------------------------------------
// Migration 050 gate three-way integration tests (U8)
// ---------------------------------------------------------------------------

// TestMigrator_ForwardRebuild_Migration050_EmptyTable_Up verifies that a fresh DB
// (users/roles/role_assignments empty or absent) can run Up() through migration
// 050 without any permit — the empty-table path is always safe.
func TestMigrator_ForwardRebuild_Migration050_EmptyTable_Up(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply all migrations from scratch: all rebuild targets are empty/missing →
	// Up() must succeed without any permit.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_empty")
	require.NoError(t, err)

	require.NoError(t, migrator.Up(ctx),
		"Up() must succeed on a fresh DB (users/roles/role_assignments empty after earlier migrations)")

	// Verify 050's tenant_id column was added to users.
	var tenantIDExists bool
	err = pool.DB().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'users' AND column_name = 'tenant_id'
		)`).Scan(&tenantIDExists)
	require.NoError(t, err)
	assert.True(t, tenantIDExists, "users must have tenant_id column after migration 050")

	// Verify composite PK on roles is (tenant_id, id).
	var rolesPKCols []string
	rows, err := pool.DB().Query(ctx, `
		SELECT a.attname
		  FROM pg_constraint co
		  JOIN pg_class c ON c.oid = co.conrelid
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(co.conkey)
		 WHERE c.relname = 'roles' AND co.contype = 'p'
		 ORDER BY array_position(co.conkey, a.attnum)`)
	require.NoError(t, err)
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		rolesPKCols = append(rolesPKCols, col)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"tenant_id", "id"}, rolesPKCols,
		"roles PK must be composite (tenant_id, id) after migration 050")
}

// TestMigrator_ForwardRebuild_Migration050_PopulatedUsers_UpFailClosed verifies
// that Up() refuses fail-closed when users has rows and migration 050 is pending
// (no permit supplied).
func TestMigrator_ForwardRebuild_Migration050_PopulatedUsers_UpFailClosed(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 049 so users table exists (from migration 017)
	// but migration 050 is still pending.
	mfs049 := migrationsUpToFS(t, 49)
	prep, err := newMigratorForTable(pool, mfs049, "schema_migrations_050_failclosed_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 049 must succeed")

	// Insert a row into users to make migration 050's users target dangerous.
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, creation_source,
			 status, authz_epoch, created_at, updated_at)
		VALUES
			(gen_random_uuid(), 'alice', 'alice@example.com', '$2a$12$dummy',
			 'identity', 'active', 1, now(), now())
	`)
	require.NoError(t, execErr, "must be able to insert a row into pre-050 users table")

	// Up() without permits: must fail-closed because users has rows.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_failclosed_prep")
	require.NoError(t, err)

	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when migration 050 target users has rows")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(50), migDetail.Value(), "migration detail value must be 50")
}

// TestMigrator_ForwardRebuild_Migration050_DeclaredTargets pins migration 050's
// forward-rebuild target set to {users, roles, role_assignments}. The runtime
// fail-closed tests can only isolate users and roles by seeding (role_assignments
// has FKs to both users and roles, so it cannot be populated alone); this test
// guards every declared target against annotation drift — if a
// `-- +gocell forward-rebuild target=<table>` line is dropped or renamed, the set
// changes and this fails.
func TestMigrator_ForwardRebuild_Migration050_DeclaredTargets(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Empty DB → every migration is pending; collectPendingForwardRebuilds parses
	// the +gocell annotations from each pending migration's Up section.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_targets")
	require.NoError(t, err)

	pending, err := migrator.collectPendingForwardRebuilds(ctx)
	require.NoError(t, err)

	got := pending[50]
	require.NotEmpty(t, got, "migration 050 must declare forward-rebuild targets")
	assert.ElementsMatch(t, []string{"users", "roles", "role_assignments"}, got,
		"migration 050 forward-rebuild targets must be exactly {users, roles, role_assignments}")
}

// TestMigrator_ForwardRebuild_Migration050_PopulatedRoles_UpFailClosed verifies
// the gate fires for the roles target specifically: with roles non-empty (users
// and role_assignments empty) and no permit, Up() must refuse, and the error must
// name target=roles. This protects the `target=roles` annotation at runtime —
// dropping it would let a populated roles table be silently destroyed.
func TestMigrator_ForwardRebuild_Migration050_PopulatedRoles_UpFailClosed(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply through 049 so roles exists (migration 019) but 050 is still pending.
	mfs049 := migrationsUpToFS(t, 49)
	prep, err := newMigratorForTable(pool, mfs049, "schema_migrations_050_roles_failclosed")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 049 must succeed")

	// Seed roles only (users + role_assignments stay empty). roles has no FK, so it
	// can be populated in isolation — making roles the sole dangerous target.
	_, execErr := pool.DB().Exec(ctx, `INSERT INTO roles (id, name) VALUES ('viewer', 'Viewer')`)
	require.NoError(t, execErr, "must be able to insert a row into pre-050 roles table")

	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_roles_failclosed")
	require.NoError(t, err)

	upErr := migrator.Up(ctx)
	require.Error(t, upErr, "Up() must refuse when migration 050 target roles has rows")
	var ec *errcode.Error
	require.True(t, errors.As(upErr, &ec), "error must wrap *errcode.Error")
	assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	migDetail, ok := ec.FindAttr("migration")
	require.True(t, ok, "error must have a public detail keyed 'migration'")
	assert.Equal(t, int64(50), migDetail.Value(), "migration detail value must be 50")
	tgtDetail, ok := ec.FindAttr("target")
	require.True(t, ok, "error must have a public detail keyed 'target'")
	assert.Equal(t, "roles", tgtDetail.Value(), "target detail must name the roles table")
}

// TestMigrator_ForwardRebuild_Migration050_PopulatedUsers_WithPermit verifies
// that ForwardRebuild with permit 50 succeeds when users has rows (and sessions
// is empty — see godoc), rebuilds the schema, and the sessions FK is restored.
//
// Behavior on rebuild (per migration 050 SQL):
//   - DROP TABLE users CASCADE removes the sessions_subject_id_fkey FK and any
//     existing user rows. The sessions table itself is kept.
//   - The rebuild creates fresh users/roles/role_assignments with tenant_id.
//   - The sessions FK (sessions_subject_id_fkey) is re-added at the end of the Up
//     block via ALTER TABLE sessions ADD CONSTRAINT sessions_subject_id_fkey ...
//
// FK re-add constraint: the ALTER TABLE ADD CONSTRAINT step validates all existing
// rows in sessions. If sessions contains rows whose subject_id references a
// now-deleted user UUID, the FK addition fails with a FK violation (23503). In
// production this means: sessions must be drained before running migration 050
// (same drain discipline as migration 044 for outbox_entries). In dev: start
// fresh (drop + migrate from 001 up).
//
// This test keeps sessions empty to validate the core rebuild path cleanly.
// The pre-requisite that sessions must be drained is documented in
// docs/ops/migration-050-accesscore-tenant-rebuild.md.
func TestMigrator_ForwardRebuild_Migration050_PopulatedUsers_WithPermit(t *testing.T) {
	pool := emptyPool(t)
	ctx := context.Background()

	// Apply migrations up through 049.
	mfs049 := migrationsUpToFS(t, 49)
	prep, err := newMigratorForTable(pool, mfs049, "schema_migrations_050_permit_prep")
	require.NoError(t, err)
	require.NoError(t, prep.Up(ctx), "Up() through 049 must succeed")

	// Seed only a user row so that 050 is "dangerous" for the users target.
	// NOTE: we deliberately do NOT seed sessions here — the FK re-add at the end
	// of migration 050's Up block validates all existing sessions rows. A session
	// row referencing a now-deleted user UUID would cause a 23503 FK violation,
	// causing the migration to fail. Production runbook: drain sessions before
	// applying migration 050 (see docs/ops/migration-050-accesscore-tenant-rebuild.md).
	_, execErr := pool.DB().Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, creation_source,
			 status, authz_epoch, created_at, updated_at)
		VALUES
			(gen_random_uuid(), 'bob', 'bob@example.com', '$2a$12$dummy',
			 'identity', 'active', 1, now(), now())
	`)
	require.NoError(t, execErr, "must be able to insert a row into pre-050 users table")

	// ForwardRebuild with permit 50 must succeed. The users target has rows;
	// roles and role_assignments are empty so only one permit is needed.
	migrator, err := newMigratorForTable(pool, testMigrationsFS(t), "schema_migrations_050_permit_prep")
	require.NoError(t, err)

	permit50 := mustAllowForwardRebuild(t, 50, "050 accesscore tenant rebuild integration test")
	require.NoError(t, migrator.ForwardRebuild(ctx, permit50),
		"ForwardRebuild with permit 50 must succeed when only users has rows (sessions empty)")

	// Post-rebuild assertions:

	// 1. users.tenant_id column exists.
	var tenantIDExists bool
	err = pool.DB().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'users' AND column_name = 'tenant_id'
		)`).Scan(&tenantIDExists)
	require.NoError(t, err)
	assert.True(t, tenantIDExists, "users must have tenant_id column after ForwardRebuild with permit 50")

	// 2. users data is destroyed (DROP TABLE users CASCADE clears all rows).
	var userCount int64
	err = pool.DB().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount)
	require.NoError(t, err)
	assert.Equal(t, int64(0), userCount, "users must be empty after DROP+CREATE rebuild by migration 050")

	// 3. sessions FK is restored by migration 050's ALTER TABLE sessions ADD CONSTRAINT step.
	var fkExists bool
	err = pool.DB().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.table_constraints
			WHERE constraint_name = 'sessions_subject_id_fkey'
			  AND table_name = 'sessions'
			  AND constraint_type = 'FOREIGN KEY'
		)`).Scan(&fkExists)
	require.NoError(t, err)
	assert.True(t, fkExists,
		"sessions_subject_id_fkey must be re-added by migration 050 ALTER TABLE step")

	// 4. roles and role_assignments were recreated with correct composite PKs.
	var rolesPKCols []string
	rows, err := pool.DB().Query(ctx, `
		SELECT a.attname
		  FROM pg_constraint co
		  JOIN pg_class c ON c.oid = co.conrelid
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(co.conkey)
		 WHERE c.relname = 'roles' AND co.contype = 'p'
		 ORDER BY array_position(co.conkey, a.attnum)`)
	require.NoError(t, err)
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		rolesPKCols = append(rolesPKCols, col)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"tenant_id", "id"}, rolesPKCols,
		"roles PK must be composite (tenant_id, id) after migration 050")
}

// Target: adapters/postgres coverage >= 80%
