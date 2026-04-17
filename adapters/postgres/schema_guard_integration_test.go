//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerifyExpectedVersion_Integration verifies that after applying all
// migrations, VerifyExpectedVersion returns nil (versions match).
func TestVerifyExpectedVersion_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations first.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// VerifyExpectedVersion should pass: DB version == FS max version.
	err = VerifyExpectedVersion(ctx, pool, MigrationsFS(), "schema_migrations")
	assert.NoError(t, err, "VerifyExpectedVersion should return nil after full Up()")
}

// TestDetectInvalidIndexes_WithInjectedInvalid verifies that DetectInvalidIndexes
// returns the name of an index that has been manually marked as invalid via
// a direct UPDATE on pg_index. This requires superuser (testcontainers PG
// default user is superuser).
func TestDetectInvalidIndexes_WithInjectedInvalid(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply migrations to create tables/indexes.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_invalid_idx")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	// Verify no INVALID indexes before injection.
	before, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, before, "should have no invalid indexes before injection")

	// Inject an INVALID index by marking idx_outbox_pending_v2 as invalid.
	// We use pg_index system catalog directly (requires superuser).
	_, execErr := pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = false
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
	require.NoError(t, execErr, "injecting invalid index must succeed (requires superuser)")

	// DetectInvalidIndexes should now report it.
	after, err := DetectInvalidIndexes(ctx, pool)
	require.NoError(t, err)
	assert.NotEmpty(t, after, "should detect the injected invalid index")
	var found bool
	for _, idx := range after {
		if idx.Index == "idx_outbox_pending_v2" {
			found = true
			assert.NotEmpty(t, idx.Table, "invalid index should include table name")
			break
		}
	}
	assert.True(t, found, "invalid index list should contain the injected index")

	// Restore the index to valid state so container cleanup is clean.
	_, _ = pool.DB().Exec(ctx,
		`UPDATE pg_index SET indisvalid = true
		 WHERE indexrelid = 'idx_outbox_pending_v2'::regclass`)
}

// TestVerifyExpectedVersion_DBAhead_Integration verifies that when the DB
// schema is ahead of the binary (DB version > FS max), VerifyExpectedVersion
// returns an error containing "schema version mismatch". This simulates a
// binary rollback without a corresponding migration rollback.
func TestVerifyExpectedVersion_DBAhead_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	const tbl = "schema_migrations_ahead"

	// Apply all migrations.
	migrator, err := NewMigrator(pool, MigrationsFS(), tbl)
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "initial Up() must succeed")

	// Determine the expected (FS max) version.
	expected, err := ExpectedVersion(MigrationsFS())
	require.NoError(t, err)
	require.Greater(t, expected, int64(0), "test requires at least 1 migration")

	// Simulate a "binary rollback" scenario: DB has an extra version applied that
	// the current binary doesn't know about (expected + 1).
	_, execErr := pool.DB().Exec(ctx,
		"INSERT INTO "+tbl+" (version_id, is_applied, tstamp) VALUES ($1, true, NOW())",
		expected+1)
	require.NoError(t, execErr, "inserting extra version record must succeed")

	// VerifyExpectedVersion must now return a schema mismatch error (DB ahead).
	err = VerifyExpectedVersion(ctx, pool, MigrationsFS(), tbl)
	require.Error(t, err, "should return error when DB version is ahead of binary")
	assert.Contains(t, err.Error(), "schema version mismatch",
		"error message should mention schema version mismatch")
}

// TestVerifyExpectedVersion_DBLagged_Integration verifies that when the DB
// schema is behind the binary (DB version < FS max), VerifyExpectedVersion
// returns an error containing "schema version mismatch".
func TestVerifyExpectedVersion_DBLagged_Integration(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_lagged")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "initial Up() must succeed")

	// Determine the current max version so we can delete newer records.
	expected, err := ExpectedVersion(MigrationsFS())
	require.NoError(t, err)
	require.Greater(t, expected, int64(3),
		"test requires at least 4 migrations to simulate lag")

	// Simulate lag: remove entries for versions > 3 from the tracking table.
	_, execErr := pool.DB().Exec(ctx,
		"DELETE FROM schema_migrations_lagged WHERE version_id > 3")
	require.NoError(t, execErr, "deleting version records should succeed")

	// VerifyExpectedVersion must now return a schema mismatch error.
	err = VerifyExpectedVersion(ctx, pool, MigrationsFS(), "schema_migrations_lagged")
	require.Error(t, err, "should return error when DB is lagged")
	assert.Contains(t, err.Error(), "schema version mismatch",
		"error message should mention schema version mismatch")
}
