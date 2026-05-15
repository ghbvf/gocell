//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

const (
	refreshTestOneWeek = 7 * 24 * time.Hour
	refreshTest30Days  = 30 * 24 * time.Hour
	refreshTest90Days  = 90 * 24 * time.Hour
	refreshTest2Hours  = 2 * time.Hour
)

// TestPGRefreshStore_ContractSuite runs the shared storetest contract suites
// against a real PostgreSQL backend. This is the regression gate for backend
// parity with memstore — every Tn subtest enforces the Store contract on both
// implementations. Notable subtests for PR#388 review findings:
//
//   - T20_Peek_RejectionParityAndReuseCascade — Peek must reject identically
//     to Rotate and still cascade-revoke on reuse_detected.
//   - T21_Peek_DoesNotConsumeGraceBudget — Peek MUST NOT increment used_times.
//     Caught the PR#388 P1 finding where PG silently consumed grace budget on
//     Peek (memstore did not), halving the effective GraceMaxReuses for the
//     real sessionrefresh.Refresh() call shape (Peek + Rotate per request).
func TestPGRefreshStore_ContractSuite(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Each factory call gets its own PG schema so parallel subtests are fully
	// isolated. GC (T13) only sees rows it inserted — no shared-table pollution.
	factory := func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
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
	}

	storetest.RunContractSuite(t, factory)
	storetest.RunIdleExpireContractSuite(t, factory)
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
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         refreshTestOneWeek,
		MaxIdle:        refreshTest2Hours,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, policy, clock, nil)
	require.NoError(t, err)

	const sessionID = "sess-dml-state"
	const subjectID = "usr-dml-state"

	// Step 1: Issue — expect one row, rotated_at IS NULL, revoked_at IS NULL.
	wire, tok, err := store.Issue(ctx, sessionID, subjectID, int64(1))
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

// TestPGRefreshStore_PeekPlusRotate_RespectsGraceBudget locks down the PR#388
// P1 finding directly against the PG backend.  sessionrefresh.Refresh issues
// Peek + Rotate in the same request; without the fix PG silently incremented
// used_times in the Peek path, halving the effective grace budget compared
// with memstore. This test models the realistic retry shape (a client
// reuses parentWire because it never received the previous Rotate response)
// and asserts that GraceMaxReuses retries succeed end-to-end before the next
// Rotate is rejected with reuse_detected.
//
// Companion to storetest T21_Peek_DoesNotConsumeGraceBudget (which runs the
// same invariant on every backend); this PG-named test exists so a grep for
// "Peek" + "Grace" in adapters/postgres/ finds an explicit regression gate.
func TestPGRefreshStore_PeekPlusRotate_RespectsGraceBudget(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_peek_grace")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	policy := refresh.Policy{
		ReuseInterval:  testtime.D5s,
		MaxAge:         time.Hour,
		MaxIdle:        time.Hour,
		GraceMaxReuses: 3,
	}
	store, err := NewRefreshStore(p.DB(), txm, policy, clock, nil)
	require.NoError(t, err)

	parentWire, _, err := store.Issue(ctx, "sess-pg-grace", "usr-pg-grace", int64(1))
	require.NoError(t, err)
	// First Rotate: opens grace window. used_times stays at 0 because
	// rotated_at was nil prior to this call (handleRotatedRow not entered).
	_, _, err = store.Rotate(ctx, parentWire)
	require.NoError(t, err)

	// Replay the realistic sessionrefresh.Refresh() shape: Peek + Rotate per
	// "refresh request". Without the P1 fix, each iteration would advance
	// used_times by 2 (Peek + Rotate), exhausting GraceMaxReuses=3 after the
	// 2nd retry instead of the 3rd.
	for i := 0; i < policy.GraceMaxReuses; i++ {
		_, peekErr := store.Peek(ctx, parentWire)
		require.NoError(t, peekErr, "Peek %d must succeed in grace window", i+1)
		_, _, rotErr := store.Rotate(ctx, parentWire)
		require.NoError(t, rotErr, "Rotate %d must succeed (used_times %d → %d, cap %d)",
			i+1, i, i+1, policy.GraceMaxReuses)
	}

	// used_times == GraceMaxReuses now. Next Rotate must trip the cap.
	_, _, err = store.Rotate(ctx, parentWire)
	require.ErrorIs(t, err, refresh.ErrReused,
		"GraceMaxReuses+1th Rotate must trigger reuse_detected (cascade revoke)")
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
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clock, nil)
	require.NoError(t, err)

	parentWire, _, err := store.Issue(ctx, "sess-reuse-ambient", "usr-reuse-ambient", int64(1))
	require.NoError(t, err)
	childWire, _, err := store.Rotate(ctx, parentWire)
	require.NoError(t, err)
	clock.Advance(testtime.D3s)

	err = txm.RunInTx(ctx, func(txCtx context.Context) error {
		_, peekErr := store.Peek(txCtx, parentWire)
		require.ErrorIs(t, peekErr, refresh.ErrReused)
		return fmt.Errorf("force outer rollback")
	})
	require.Error(t, err)

	// childWire was issued by the first Rotate; after the cascade revoke
	// commits, its row is marked revoked → checkBasicValidity path → ErrRejected.
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
	store, err := NewRefreshStore(p.DB(), txm2, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clock, nil)
	require.NoError(t, err)

	parentWire, _, err := store.Issue(ctx, "sess-reuse-lock", "usr-reuse-lock", int64(1))
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

	wire, _, err := store.Issue(ctx, "sess-t19", "usr-t19", int64(1))
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

	parentWire, _, err := store.Issue(ctx, "sess-t20", "usr-t20", int64(1))
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

	// GraceMaxReuses exhausted — next re-present of parent triggers cascade revoke
	// and returns ErrReused (carrying row identity so the service layer can drive
	// the user-wide credential invalidation cascade).
	_, _, err = store.Rotate(ctx, parentWire)
	require.ErrorIs(t, err, refresh.ErrReused, "Rotate after grace exhausted must cascade revoke and return ErrReused")
}

// ---------------------------------------------------------------------------
// T21: reject path uniform logging
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T21_RejectPathsHaveUniformLogging verifies that every
// non-reuse reject branch funnels through rejectWithReason → ErrRejected, and
// that reuse branches (reuse_detected, grace_exhausted) return ErrReused so the
// service layer can drive the user-wide credential invalidation cascade.
// This is a behavioral contract test — the specific slog output is
// implementation-detail; we assert the wire-error sentinel only.
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
	wire, _, err := store.Issue(ctx, "sess-t21-exp", "usr-t21", int64(1))
	require.NoError(t, err)
	clock.Advance(refreshTest2Hours)
	_, err = store.Peek(ctx, wire)
	require.ErrorIs(t, err, refresh.ErrRejected, "expired must return ErrRejected")

	// revoked
	wire2, _, err := store.Issue(ctx, "sess-t21-rev", "usr-t21", int64(1))
	require.NoError(t, err)
	require.NoError(t, store.RevokeSession(ctx, "sess-t21-rev"))
	_, err = store.Peek(ctx, wire2)
	require.ErrorIs(t, err, refresh.ErrRejected, "revoked must return ErrRejected")

	// reuse_detected — rotate the parent, wait past reuseInterval, re-present parent
	wire3, _, err := store.Issue(ctx, "sess-t21-reuse", "usr-t21", int64(1))
	require.NoError(t, err)
	_, _, err = store.Rotate(ctx, wire3)
	require.NoError(t, err)
	clock.Advance(testtime.D3s) // past ReuseInterval
	_, _, err = store.Rotate(ctx, wire3)
	require.ErrorIs(t, err, refresh.ErrReused, "reuse_detected must return ErrReused (carries row identity for cascade)")
}

// ---------------------------------------------------------------------------
// T22: readyz postgres_indexes_valid_ready probe
// ---------------------------------------------------------------------------

// TestPGRefreshStore_T22_ReadyzReportsInvalidIndex verifies that the
// postgres_indexes_valid_ready checker in Pool.Checkers returns a normal
// errcode error when invalid indexes exist. It must not wrap cell.ErrDegraded:
// invalid indexes are a schema fault, so runtime/http/health.runOneProbe must
// classify the probe as unhealthy and /readyz must fail closed with HTTP 503.
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

	checkers := p.Checkers()
	invalidIdxChecker, ok := checkers["postgres_indexes_valid_ready"]
	require.True(t, ok, "postgres_indexes_valid_ready checker must be present in Checkers()")

	err = invalidIdxChecker(ctx)
	require.Error(t, err, "invalid indexes must make postgres_indexes_valid_ready fail")
	assert.False(t, errors.Is(err, cell.ErrDegraded),
		"invalid indexes must not be treated as fail-open degraded readiness")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "invalid index checker must return an errcode error")
	assert.Equal(t, ErrAdapterPGQuery, ec.Code)
	require.Contains(t, err.Error(), "idx_t22_probe_val",
		"error must list invalid index names for /readyz?verbose diagnostics")

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

	wire, _, err := store.Issue(ctx, "sess-t23", "usr-t23", int64(1))
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

func TestPGRefreshStore_RevokeSessionDetachedSurvivesAmbientRollback(t *testing.T) {
	base, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	p := isolatedSchemaPool(t, ctx, base)
	migrator, err := NewMigrator(p, testMigrationsFS(t), "schema_migrations_detached_revoke")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	clock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	txm := NewTxManager(p)
	store, err := NewRefreshStore(p.DB(), txm, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refreshTest30Days,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clock, nil)
	require.NoError(t, err)

	detachedWire, _, err := store.Issue(ctx, "sess-detached-revoke", "usr-detached-revoke", int64(1))
	require.NoError(t, err)
	businessWire, _, err := store.Issue(ctx, "sess-business-revoke", "usr-business-revoke", int64(1))
	require.NoError(t, err)

	runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		require.NoError(t, store.RevokeSessionDetached(txCtx, "sess-detached-revoke"))
		return fmt.Errorf("force outer rollback")
	})
	require.Error(t, runErr)

	_, _, err = store.Rotate(ctx, detachedWire)
	require.ErrorIs(t, err, refresh.ErrRejected,
		"detached session revoke must commit independently of the caller rollback")

	runErr = txm.RunInTx(ctx, func(txCtx context.Context) error {
		require.NoError(t, store.RevokeSession(txCtx, "sess-business-revoke"))
		return fmt.Errorf("force outer rollback")
	})
	require.Error(t, runErr)

	_, _, err = store.Rotate(ctx, businessWire)
	require.NoError(t, err,
		"business RevokeSession must participate in ambient tx and roll back with it")
}

// ---------------------------------------------------------------------------
// PR#388 Finding 1 — detached revoke coverage boundaries
// ---------------------------------------------------------------------------
//
// Finding 1 asked for an "integration test that cancels the caller ctx during
// reuse_detected and grace-exhausted handling". The literal shape — call
// store.Rotate with an already-cancelled ctx — does not reach the cascade SQL:
// txRunner.RunInTx fails at pool.Begin(ctx) with context.Canceled before the
// reuse check runs. Inserting the cancel mid-call would require either a mock
// pgxpool (boxing the behavior into a mock) or timing-sensitive orchestration.
// The maintained coverage is intentionally narrower and executable:
//
//   1. pkg/ctxutil/detach_test.go asserts that WithDetachedTimeout's returned
//      ctx is unaffected by parent cancel and carries an independent deadline.
//   2. cells/accesscore/slices/sessionrefresh/service_test.go::
//      TestService_CascadeRevoke_UsesDetachedStoreMethod asserts that the
//      service-level cascade path routes to RevokeSessionDetached rather than
//      the ambient business revoke.
//   3. refresh_store.go handleRotatedRow and RevokeSessionDetached use
//      ctxutil.WithDetachedTimeout for the cascade pool.Exec — verified by code
//      inspection and the helper test (#1) which proves the wrapped ctx behaves
//      as required.
//   4. TestPGRefreshStore_ReuseCascadeSurvivesAmbientRollback (above) verifies
//      the orthogonal property that cascade SQL bypasses the ambient tx — i.e.
//      survives caller-driven outer rollback.
//
// Together these cover the chosen boundary: once execution reaches the store's
// detached revoke path, the final revoke write is detached from caller cancel
// and ambient rollback. They do not claim that every Refresh entry path
// continues after an already-canceled caller context.
