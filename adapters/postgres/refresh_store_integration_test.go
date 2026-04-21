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
