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
		migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations")
		require.NoError(t, err)
		require.NoError(t, migrator.Up(ctx))

		clock := storetest.NewFakeClock(baseTime)
		store := MustNewRefreshStore(p.DB(), policy, clock, nil)
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

	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_012_struct")
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

// ---------------------------------------------------------------------------
// TestPGRefreshStore_DMLState — F16: verify raw DB state after Issue → Rotate → RevokeSession
// ---------------------------------------------------------------------------

// TestPGRefreshStore_DMLState asserts the append-only row state after each
// mutating operation: Issue, Rotate, and RevokeSession.
func TestPGRefreshStore_DMLState(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_dml_state")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	policy := refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: 7 * 24 * time.Hour}
	store := MustNewRefreshStore(p.DB(), policy, clock, nil)

	const sessionID = "sess-dml-state"
	const subjectID = "usr-dml-state"

	// Step 1: Issue — expect one row, rotated_at IS NULL, revoked_at IS NULL.
	wire, tok, err := store.Issue(ctx, sessionID, subjectID)
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	require.NotNil(t, tok)

	t.Run("after_Issue_one_row_no_flags", func(t *testing.T) {
		var rowCount int
		var rotatedAt, revokedAt *time.Time
		err := p.DB().QueryRow(ctx,
			`SELECT count(*), max(rotated_at), max(revoked_at)
			 FROM refresh_tokens WHERE session_id = $1`, sessionID).
			Scan(&rowCount, &rotatedAt, &revokedAt)
		require.NoError(t, err)
		assert.Equal(t, 1, rowCount, "Issue must insert exactly one row")
		assert.Nil(t, rotatedAt, "after Issue rotated_at must be NULL")
		assert.Nil(t, revokedAt, "after Issue revoked_at must be NULL")
	})

	// Step 2: Rotate — expect TWO rows; original has rotated_at IS NOT NULL;
	// child row's parent_id equals the original row's id.
	newWire, newTok, err := store.Rotate(ctx, wire)
	require.NoError(t, err)
	require.NotEmpty(t, newWire)
	require.NotNil(t, newTok)

	t.Run("after_Rotate_two_rows_parent_rotated", func(t *testing.T) {
		var rowCount int
		err := p.DB().QueryRow(ctx,
			`SELECT count(*) FROM refresh_tokens WHERE session_id = $1`, sessionID).
			Scan(&rowCount)
		require.NoError(t, err)
		assert.Equal(t, 2, rowCount, "Rotate must append a child row (total = 2)")

		// Original row must be rotated.
		var origRotatedAt *time.Time
		err = p.DB().QueryRow(ctx,
			`SELECT rotated_at FROM refresh_tokens WHERE id = $1`, tok.ID).
			Scan(&origRotatedAt)
		require.NoError(t, err)
		assert.NotNil(t, origRotatedAt, "original row rotated_at must be set after Rotate")

		// Child row must point back to the original (parent_id = original.id).
		var parentID interface{}
		err = p.DB().QueryRow(ctx,
			`SELECT parent_id FROM refresh_tokens WHERE id = $1`, newTok.ID).
			Scan(&parentID)
		require.NoError(t, err)
		assert.NotNil(t, parentID, "child row parent_id must equal the original row id")
	})

	// Step 3: RevokeSession — ALL rows for the session must have revoked_at IS NOT NULL.
	require.NoError(t, store.RevokeSession(ctx, sessionID))

	t.Run("after_RevokeSession_all_rows_revoked", func(t *testing.T) {
		var unrevokedCount int
		err := p.DB().QueryRow(ctx,
			`SELECT count(*) FROM refresh_tokens WHERE session_id = $1 AND revoked_at IS NULL`, sessionID).
			Scan(&unrevokedCount)
		require.NoError(t, err)
		assert.Zero(t, unrevokedCount, "after RevokeSession all rows must have revoked_at set")
	})
}

func TestPGRefreshStore_ReuseCascadeSurvivesAmbientRollback(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_reuse_ambient")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := MustNewRefreshStore(p.DB(), refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: time.Hour}, clock, nil)
	txm := NewTxManager(p)

	parentWire, _, err := store.Issue(ctx, "sess-reuse-ambient", "usr-reuse-ambient")
	require.NoError(t, err)
	childWire, _, err := store.Rotate(ctx, parentWire)
	require.NoError(t, err)
	clock.Advance(3 * time.Second)

	err = txm.RunInTx(ctx, func(txCtx context.Context) error {
		_, peekErr := store.Peek(txCtx, parentWire)
		require.ErrorIs(t, peekErr, refresh.ErrRejected)
		return fmt.Errorf("force outer rollback")
	})
	require.Error(t, err)

	_, _, err = store.Rotate(ctx, childWire)
	require.ErrorIs(t, err, refresh.ErrRejected, "reuse cascade must commit independently of caller rollback")
}

func TestPGRefreshStore_SessionLockRejectsChildValidatedBeforeCascade(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_reuse_lock")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := MustNewRefreshStore(p.DB(), refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: time.Hour}, clock, nil)

	parentWire, _, err := store.Issue(ctx, "sess-reuse-lock", "usr-reuse-lock")
	require.NoError(t, err)
	childWire, _, err := store.Rotate(ctx, parentWire)
	require.NoError(t, err)

	tx, err := p.DB().Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, lockSessionSQL, "sess-reuse-lock")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, _, rotateErr := store.Rotate(ctx, childWire)
		done <- rotateErr
	}()

	time.Sleep(100 * time.Millisecond)
	_, err = tx.Exec(ctx, revokeSessionSQL, clock.Now(), "sess-reuse-lock")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	select {
	case err := <-done:
		require.ErrorIs(t, err, refresh.ErrRejected)
	case <-time.After(2 * time.Second):
		t.Fatal("Rotate(child) did not unblock after reuse cascade committed")
	}
}
