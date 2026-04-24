//go:build integration

package postgres

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGRefreshStore_ContractSuite(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Each factory call gets its own PG schema so parallel subtests are fully
	// isolated. GC (T13) only sees rows it inserted — no shared-table pollution.
	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		t.Helper()

		p := isolatedSchemaPool(t, ctx, base)
		migrator, err := NewMigrator(p, MigrationsFS(), "schema_migrations")
		require.NoError(t, err)
		require.NoError(t, migrator.Up(ctx))

		clock := storetest.NewFakeClock(baseTime)
		store := NewRefreshStore(p.DB(), policy, clock, nil)
		return store, clock
	})
}

// isolatedSchemaPool creates a fresh PG schema and returns a Pool whose
// search_path is scoped to that schema. t.Cleanup drops the schema and closes
// the pool after the subtest finishes.
func isolatedSchemaPool(t *testing.T, ctx context.Context, base *Pool) *Pool {
	t.Helper()

	schema := fmt.Sprintf("ts%016x", rand.Int63())
	_, err := base.DB().Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = base.DB().Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	cfg := base.DB().Config()
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	inner, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)

	p := &Pool{inner: inner, config: base.config}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// ---------------------------------------------------------------------------
// TestMigration012_StructuralAssertions
// ---------------------------------------------------------------------------

// TestMigration012_StructuralAssertions verifies the column layout and index
// set of the refresh_tokens table after migration 012 rebuilds it for the
// append-only selector/verifier model.
func TestMigration012_StructuralAssertions(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_012_struct")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations through 012")

	// --- table exists ---
	t.Run("refresh_tokens_table_exists", func(t *testing.T) {
		var exists bool
		err := pool.DB().QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'refresh_tokens')").
			Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "refresh_tokens table must exist after migration 012")
	})

	// --- columns (new append-only schema) ---
	wantColumns := []string{
		"id", "parent_id", "session_id", "subject_id",
		"selector", "verifier_hash",
		"created_at", "expires_at", "rotated_at", "revoked_at",
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

	// --- old columns must be gone ---
	deletedColumns := []string{"token", "obsolete_token", "last_used"}
	for _, col := range deletedColumns {
		col := col
		t.Run("refresh_tokens_missing_col_"+col, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				`SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'refresh_tokens' AND column_name = $1
				)`, col).Scan(&exists)
			require.NoError(t, err)
			assert.Falsef(t, exists, "refresh_tokens must NOT have legacy column %q", col)
		})
	}

	// --- indexes (new names) ---
	wantIndexes := []string{
		"idx_refresh_tokens_selector_live",
		"idx_refresh_tokens_selector",
		"idx_refresh_tokens_session",
		"idx_refresh_tokens_subject",
		"idx_refresh_tokens_expires",
		"idx_refresh_tokens_parent",
	}
	for _, idx := range wantIndexes {
		idx := idx
		t.Run("index_exists_"+idx, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)", idx).
				Scan(&exists)
			require.NoError(t, err)
			assert.Truef(t, exists, "index %q must exist after migration 012", idx)
		})
	}

	// --- legacy indexes must be gone ---
	deletedIndexes := []string{
		"idx_refresh_tokens_token_active",
		"idx_refresh_tokens_obsolete_active",
		"idx_refresh_tokens_token",
	}
	for _, idx := range deletedIndexes {
		idx := idx
		t.Run("index_missing_"+idx, func(t *testing.T) {
			var exists bool
			err := pool.DB().QueryRow(ctx,
				"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)", idx).
				Scan(&exists)
			require.NoError(t, err)
			assert.Falsef(t, exists, "legacy index %q must NOT exist after migration 012", idx)
		})
	}
}
