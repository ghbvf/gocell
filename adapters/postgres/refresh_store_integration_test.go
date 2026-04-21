//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGRefreshStore_ContractSuite(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	// Run migrations using a dedicated tracking table so this test's schema
	// version tracking does not collide with other integration test suites.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_refresh_store")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		t.Helper()
		// No TRUNCATE: storetest assigns unique session IDs per test case
		// ("sess-1".."sess-13b") and unique random tokens, so parallel subtests
		// do not collide. The container is fresh per TestPGRefreshStore_ContractSuite
		// invocation, so stale rows from a previous run are not a concern.

		clock := storetest.NewFakeClock(baseTime)
		store := NewRefreshStore(pool.DB(), policy, clock, nil)
		return store, clock
	})
}

// ---------------------------------------------------------------------------
// TestMigration007_StructuralAssertions
// ---------------------------------------------------------------------------

// TestMigration007_StructuralAssertions verifies the column layout and index
// set of the refresh_tokens table after migration 007 is applied
// (P2-Cx2 structural evidence).
func TestMigration007_StructuralAssertions(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_007_struct")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")

	// --- table exists ---
	t.Run("refresh_tokens_table_exists", func(t *testing.T) {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'refresh_tokens')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "refresh_tokens table must exist after migration 007")
	})

	// --- columns ---
	wantColumns := []string{
		"id", "token", "obsolete_token", "session_id", "subject_id",
		"created_at", "last_used", "expires_at", "revoked_at",
	}
	for _, col := range wantColumns {
		col := col
		t.Run("refresh_tokens_has_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				`SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'refresh_tokens' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "refresh_tokens must have column %q", col)
		})
	}

	// --- indexes ---
	wantIndexes := []string{
		"idx_refresh_tokens_token_active",
		"idx_refresh_tokens_obsolete_active",
		"idx_refresh_tokens_session",
		"idx_refresh_tokens_expires",
	}
	for _, idx := range wantIndexes {
		idx := idx
		t.Run("index_exists_"+idx, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)", idx).
				Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "index %q must exist after migration 007", idx)
		})
	}
}

// ---------------------------------------------------------------------------
// TestMigration011_StructuralAssertions
// ---------------------------------------------------------------------------

// TestMigration011_StructuralAssertions verifies that migration 011 creates
// the non-partial idx_refresh_tokens_token index covering all rows
// (P2-Cx2 structural evidence).
func TestMigration011_StructuralAssertions(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_011_struct")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations including 011")

	t.Run("idx_refresh_tokens_token_exists", func(t *testing.T) {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_refresh_tokens_token')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "idx_refresh_tokens_token must exist after migration 011")
	})
}
