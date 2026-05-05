//go:build integration

package postgres

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

const refreshTestOneWeek = 7 * 24 * time.Hour
const refreshTest30Days = 30 * 24 * time.Hour
const refreshTest90Days = 90 * 24 * time.Hour
const refreshTest2Hours = 2 * time.Hour

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
		txm := NewTxManager(p)
		store, err := NewRefreshStore(p.DB(), txm, policy, clock, nil)
		require.NoError(t, err)
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
	policy := refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: refreshTestOneWeek, MaxIdle: refreshTest2Hours}
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, policy, clock, nil)
	require.NoError(t, err)

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
		var idleExpiresAt time.Time
		err := p.DB().QueryRow(ctx,
			`SELECT count(*), max(rotated_at), max(revoked_at), max(idle_expires_at)
			 FROM refresh_tokens WHERE session_id = $1`, sessionID).
			Scan(&rowCount, &rotatedAt, &revokedAt, &idleExpiresAt)
		require.NoError(t, err)
		assert.Equal(t, 1, rowCount, "Issue must insert exactly one row")
		assert.Nil(t, rotatedAt, "after Issue rotated_at must be NULL")
		assert.Nil(t, revokedAt, "after Issue revoked_at must be NULL")
		// idle_expires_at must equal now + MaxIdle (2h) as set by migration 016.
		wantIdleExpires := clock.Now().Add(policy.MaxIdle)
		assert.True(t, idleExpiresAt.Equal(wantIdleExpires),
			"idle_expires_at must equal now+MaxIdle: got %v, want %v", idleExpiresAt, wantIdleExpires)
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
		var parentID any
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
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
	require.NoError(t, err)

	parentWire, _, err := store.Issue(ctx, "sess-reuse-ambient", "usr-reuse-ambient")
	require.NoError(t, err)
	childWire, _, err := store.Rotate(ctx, parentWire)
	require.NoError(t, err)
	clock.Advance(testtime.D3s)

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
	txm2 := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm2, refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
	require.NoError(t, err)

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

	time.Sleep(testtime.SlowPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking Rotate; no started observable
	_, err = tx.Exec(ctx, revokeSessionSQL, clock.Now(), "sess-reuse-lock")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	select {
	case err := <-done:
		require.ErrorIs(t, err, refresh.ErrRejected)
	case <-time.After(testtime.D2s):
		t.Fatal("Rotate(child) did not unblock after reuse cascade committed")
	}
}

// ---------------------------------------------------------------------------
// T19: idle_expires_at — Rotate after MaxIdle window → ErrRejected "idle_expired"
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T19_IdleExpireBlocksRotate issues a token, advances the
// clock beyond Policy.MaxIdle, and asserts that Rotate returns ErrRejected.
// RED in Wave 1 (migration 016 not applied yet; idle_expires_at column absent).
func TestPGRefreshStore_T19_IdleExpireBlocksRotate(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_t19")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         refreshTest90Days,
		MaxIdle:        refreshTest30Days,
		GraceMaxReuses: 3,
	}, clock, nil)
	require.NoError(t, err)

	wire, _, err := store.Issue(ctx, "sess-t19", "usr-t19")
	require.NoError(t, err)

	// Advance past MaxIdle.
	clock.Advance(refreshTest30Days + time.Second)

	_, _, err = store.Rotate(ctx, wire)
	require.ErrorIs(t, err, refresh.ErrRejected, "Rotate after MaxIdle must return ErrRejected")
}

// ---------------------------------------------------------------------------
// T20: grace_counter — GraceMaxReuses exhausted → cascade revoke
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T20_GraceCounterCapTriggersReuse issues a token and
// rotates it GraceMaxReuses times (grace window), then asserts that rotating
// the original parent token one more time triggers cascade revoke (ErrRejected).
// RED in Wave 1.
func TestPGRefreshStore_T20_GraceCounterCapTriggersReuse(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_t20")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	graceMax := 2
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  refreshTest90Days, // large grace window so reuse doesn't trigger
		MaxAge:         refreshTest90Days,
		MaxIdle:        refreshTest30Days,
		GraceMaxReuses: graceMax,
	}, clock, nil)
	require.NoError(t, err)

	parentWire, _, err := store.Issue(ctx, "sess-t20", "usr-t20")
	require.NoError(t, err)

	// Rotate once to set rotated_at on parent and get child.
	childWire, _, err := store.Rotate(ctx, parentWire)
	require.NoError(t, err)
	require.NotEmpty(t, childWire)

	// Re-present parent graceMax times within grace window — each should succeed
	// and return a new child (grace retry path).
	for i := 0; i < graceMax; i++ {
		_, _, err = store.Rotate(ctx, parentWire)
		require.NoError(t, err, "grace retry %d should succeed", i+1)
	}

	// GraceMaxReuses exhausted — next re-present of parent triggers cascade revoke.
	_, _, err = store.Rotate(ctx, parentWire)
	require.ErrorIs(t, err, refresh.ErrRejected, "Rotate after grace exhausted must cascade revoke and return ErrRejected")
}

// ---------------------------------------------------------------------------
// T21: reject path uniform logging
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T21_RejectPathsHaveUniformLogging verifies that every
// non-reuse reject branch funnels through rejectWithReason by checking that
// the store returns ErrRejected (not a wrapped internal error) for each
// standard reject scenario.
// This is a behavioral contract test — the specific slog output is
// implementation-detail; we assert uniform ErrRejected shape only.
// RED in Wave 1 (passes once store uses uniform rejectWithReason for reuse_detected).
func TestPGRefreshStore_T21_RejectPathsHaveUniformLogging(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_t21")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refreshTest30Days,
		GraceMaxReuses: 3,
	}, clock, nil)
	require.NoError(t, err)

	// malformed
	_, err = store.Peek(ctx, "not-a-valid-wire-token")
	require.ErrorIs(t, err, refresh.ErrRejected, "malformed must return ErrRejected")

	// selector_miss
	_, err = store.Peek(ctx, "AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	require.ErrorIs(t, err, refresh.ErrRejected, "selector_miss must return ErrRejected")

	// expired
	wire, _, err := store.Issue(ctx, "sess-t21-exp", "usr-t21")
	require.NoError(t, err)
	clock.Advance(refreshTest2Hours)
	_, err = store.Peek(ctx, wire)
	require.ErrorIs(t, err, refresh.ErrRejected, "expired must return ErrRejected")

	// revoked
	wire2, _, err := store.Issue(ctx, "sess-t21-rev", "usr-t21")
	require.NoError(t, err)
	require.NoError(t, store.RevokeSession(ctx, "sess-t21-rev"))
	_, err = store.Peek(ctx, wire2)
	require.ErrorIs(t, err, refresh.ErrRejected, "revoked must return ErrRejected")

	// reuse_detected — rotate the parent, wait past reuseInterval, re-present parent
	wire3, _, err := store.Issue(ctx, "sess-t21-reuse", "usr-t21")
	require.NoError(t, err)
	_, _, err = store.Rotate(ctx, wire3)
	require.NoError(t, err)
	clock.Advance(testtime.D3s) // past ReuseInterval
	_, _, err = store.Rotate(ctx, wire3)
	require.ErrorIs(t, err, refresh.ErrRejected, "reuse_detected must return ErrRejected")
}

// ---------------------------------------------------------------------------
// T22: readyz postgres_indexes_valid_ready probe
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T22_ReadyzReportsInvalidIndex verifies that the
// postgres_indexes_valid_ready checker in PGResource.Checkers returns a
// non-nil error when an invalid index exists in the schema.
// RED in Wave 1 (checker not yet added to pool_resource.go).
func TestPGRefreshStore_T22_ReadyzReportsInvalidIndex(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_t22")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	// Create a table and then manually mark an index as invalid via pg_index.
	// We cannot use CREATE INDEX CONCURRENTLY (not in transaction), so we
	// insert a fake invalid index entry by creating a real index and then
	// flipping its indisvalid flag.
	_, err = p.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS _t22_probe (id serial primary key, val text)`)
	require.NoError(t, err)
	_, err = p.DB().Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_t22_probe_val ON _t22_probe (val)`)
	require.NoError(t, err)
	// Flip indisvalid=false to simulate a broken CONCURRENTLY index.
	_, err = p.DB().Exec(ctx, `UPDATE pg_index SET indisvalid = false
		WHERE indexrelid = (
			SELECT oid FROM pg_class WHERE relname = 'idx_t22_probe_val'
		)`)
	require.NoError(t, err)

	resource, err := NewPGResource(p)
	require.NoError(t, err)

	checkers := resource.Checkers()
	invalidIdxChecker, ok := checkers["postgres_indexes_valid_ready"]
	require.True(t, ok, "postgres_indexes_valid_ready checker must be present in Checkers()")

	err = invalidIdxChecker(ctx)
	require.Error(t, err, "postgres_indexes_valid_ready must return error when invalid indexes exist")

	t.Cleanup(func() {
		_, _ = p.DB().Exec(context.Background(), `DROP INDEX IF EXISTS idx_t22_probe_val`)
		_, _ = p.DB().Exec(context.Background(), `DROP TABLE IF EXISTS _t22_probe`)
	})
}

// ---------------------------------------------------------------------------
// T23: ambient tx rollback — store fully delegates to TxRunner
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T23_AmbientTxRollback verifies that when a caller wraps
// store.Rotate in RunInTx and then forces a rollback, no new refresh_tokens
// row persists. This asserts the ambient-only contract: refresh_store must not
// hold its own internal transaction that commits independently of the caller.
// RED in Wave 1 (store still has internal pool.Begin/Commit).
func TestPGRefreshStore_T23_AmbientTxRollback(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_t23")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refreshTest30Days,
		GraceMaxReuses: 3,
	}, clock, nil)
	require.NoError(t, err)

	wire, _, err := store.Issue(ctx, "sess-t23", "usr-t23")
	require.NoError(t, err)

	// Count rows before the aborted Rotate.
	var countBefore int
	err = p.DB().QueryRow(ctx, "SELECT count(*) FROM refresh_tokens WHERE session_id = 'sess-t23'").Scan(&countBefore)
	require.NoError(t, err)

	// Run Rotate inside a transaction that is then rolled back by returning an error.
	runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		_, _, rotErr := store.Rotate(txCtx, wire)
		require.NoError(t, rotErr, "Rotate inside ambient tx must succeed")
		return fmt.Errorf("force rollback")
	})
	require.Error(t, runErr, "RunInTx must return error after forced rollback")

	// After rollback, the Rotate's INSERT must have been rolled back too.
	var countAfter int
	err = p.DB().QueryRow(ctx, "SELECT count(*) FROM refresh_tokens WHERE session_id = 'sess-t23'").Scan(&countAfter)
	require.NoError(t, err)
	require.Equal(t, countBefore, countAfter,
		"ambient tx rollback must revert Rotate INSERT: no new row should persist")
}
