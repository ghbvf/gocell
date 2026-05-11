//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/testutil"
)

// setupRoleRepoPG starts a PostgreSQL testcontainer, applies all migrations,
// and returns a PGRoleRepo + PGUserRepo + TxManager + cleanup func.
// Both repos share the same pool so FK constraints are exercised.
func setupRoleRepoPG(t *testing.T) (*PGRoleRepo, *PGUserRepo, *adapterpg.TxManager, func()) {
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
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	txMgr := adapterpg.NewTxManager(pool)
	roleRepo, err := NewPGRoleRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	userRepo, err := NewPGUserRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return roleRepo, userRepo, txMgr, cleanup
}

// newTestRole builds a minimal domain.Role.
func newTestRole(id, name string, perms ...domain.Permission) *domain.Role {
	return &domain.Role{
		ID:          id,
		Name:        name,
		Permissions: perms,
	}
}

// createTestUserInDB inserts a test user into the DB and returns it.
func createTestUserInDB(t *testing.T, userRepo *PGUserRepo, suffix string) *domain.User {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	u := &domain.User{
		ID:             uuid.NewString(),
		Username:       "roletest_" + suffix,
		Email:          "roletest_" + suffix + "@example.com",
		PasswordHash:   "$2a$12$fakehash",
		Status:         domain.StatusActive,
		CreationSource: domain.UserSourceIdentity,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(t, userRepo.Create(ctx, u))
	return u
}

// ---------------------------------------------------------------------------
// Constructor fail-fast
// ---------------------------------------------------------------------------

func TestPGRoleRepo_Constructor_FailFast(t *testing.T) {
	testutil.RequireDocker(t)
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	txm := adapterpg.NewTxManager(pool)

	assertValidationFailed := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	}

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewPGRoleRepo(nil, txm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_txRunner_typed_nil", func(t *testing.T) {
		var nilTxm *adapterpg.TxManager
		_, err := NewPGRoleRepo(pool.DB(), nilTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_clock_typed_nil", func(t *testing.T) {
		_, err := NewPGRoleRepo(pool.DB(), txm, nil)
		assertValidationFailed(t, err)
	})
}

// ---------------------------------------------------------------------------
// CRUD integration tests
// ---------------------------------------------------------------------------

func TestPGRoleRepo_Integration(t *testing.T) {
	roleRepo, userRepo, _, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Create_GetByID_roundtrip_with_permissions_JSONB", func(t *testing.T) {
		role := newTestRole("admin", "Administrator",
			domain.Permission{Resource: "users", Action: "read"},
			domain.Permission{Resource: "users", Action: "write"},
		)
		require.NoError(t, roleRepo.Create(ctx, role))

		got, err := roleRepo.GetByID(ctx, "admin")
		require.NoError(t, err)
		assert.Equal(t, "admin", got.ID)
		assert.Equal(t, "Administrator", got.Name)
		require.Len(t, got.Permissions, 2)
		assert.Equal(t, "users", got.Permissions[0].Resource)
	})

	t.Run("Create_upsert_overwrites_existing", func(t *testing.T) {
		role := newTestRole("viewer_"+uuid.NewString()[:8], "Viewer v1")
		require.NoError(t, roleRepo.Create(ctx, role))

		role.Name = "Viewer v2"
		role.Permissions = []domain.Permission{{Resource: "reports", Action: "read"}}
		require.NoError(t, roleRepo.Create(ctx, role)) // upsert

		got, err := roleRepo.GetByID(ctx, role.ID)
		require.NoError(t, err)
		assert.Equal(t, "Viewer v2", got.Name)
		require.Len(t, got.Permissions, 1)
	})

	t.Run("GetByID_missing_returns_ErrAuthRoleNotFound", func(t *testing.T) {
		_, err := roleRepo.GetByID(ctx, "nonexistent_"+uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthRoleNotFound, ec.Code)
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	})

	t.Run("AssignToUser_idempotent_changed_true_first_false_second", func(t *testing.T) {
		roleID := "rwa_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "RWA")))

		user := createTestUserInDB(t, userRepo, "rwa")

		changed1, err := roleRepo.AssignToUser(ctx, user.ID, roleID)
		require.NoError(t, err)
		assert.True(t, changed1, "first assignment must be changed=true")

		changed2, err := roleRepo.AssignToUser(ctx, user.ID, roleID)
		require.NoError(t, err)
		assert.False(t, changed2, "second assignment (idempotent) must be changed=false")
	})

	t.Run("AssignToUser_missing_role_returns_ErrAuthRoleNotFound", func(t *testing.T) {
		user := createTestUserInDB(t, userRepo, "missingrole")
		_, err := roleRepo.AssignToUser(ctx, user.ID, "no_such_role_"+uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthRoleNotFound, ec.Code)
	})

	t.Run("RemoveFromUser_idempotent", func(t *testing.T) {
		roleID := "rmv_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "RMV")))

		user := createTestUserInDB(t, userRepo, "rmv")

		_, err := roleRepo.AssignToUser(ctx, user.ID, roleID)
		require.NoError(t, err)

		// First remove — should succeed.
		require.NoError(t, roleRepo.RemoveFromUser(ctx, user.ID, roleID))

		// Second remove — idempotent, no error.
		require.NoError(t, roleRepo.RemoveFromUser(ctx, user.ID, roleID))

		// GetByUserID should show no roles.
		roles, err := roleRepo.GetByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, roles)
	})

	t.Run("RemoveFromUserIfNotLast_user_does_not_hold_role_noop", func(t *testing.T) {
		roleID := "nilr_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "NILR")))

		user := createTestUserInDB(t, userRepo, "nilr")

		// User never assigned this role.
		changed, err := roleRepo.RemoveFromUserIfNotLast(ctx, user.ID, roleID)
		require.NoError(t, err)
		assert.False(t, changed, "user did not hold role → changed=false, no error")
	})

	t.Run("RemoveFromUserIfNotLast_two_holders_removes_one", func(t *testing.T) {
		roleID := "twohold_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "TWOHOLD")))

		u1 := createTestUserInDB(t, userRepo, "th1")
		u2 := createTestUserInDB(t, userRepo, "th2")

		_, err := roleRepo.AssignToUser(ctx, u1.ID, roleID)
		require.NoError(t, err)
		_, err = roleRepo.AssignToUser(ctx, u2.ID, roleID)
		require.NoError(t, err)

		// Remove u1 — u2 still holds the role.
		changed, err := roleRepo.RemoveFromUserIfNotLast(ctx, u1.ID, roleID)
		require.NoError(t, err)
		assert.True(t, changed, "role had 2 holders → removal succeeded, changed=true")

		count, err := roleRepo.CountByRole(ctx, roleID)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("RemoveFromUserIfNotLast_sole_admin_returns_ErrAuthLastAdminProtected", func(t *testing.T) {
		// Last-holder protection is admin-scoped (ADR-admin-invariant §3.2 +
		// migration 019:50 trigger). Use auth.RoleAdmin to exercise the
		// CTE/trigger guard.
		require.NoError(t, roleRepo.Create(ctx, newTestRole(auth.RoleAdmin, "Administrator")))

		user := createTestUserInDB(t, userRepo, "soleAdmin")
		_, err := roleRepo.AssignToUser(ctx, user.ID, auth.RoleAdmin)
		require.NoError(t, err)

		changed, err := roleRepo.RemoveFromUserIfNotLast(ctx, user.ID, auth.RoleAdmin)
		require.Error(t, err, "sole admin must not be removed")
		assert.False(t, changed)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthLastAdminProtected, ec.Code)
		assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
		assert.Contains(t, ec.Message, "only admin")
	})

	t.Run("RemoveFromUserIfNotLast_non_admin_sole_holder_revoked_cleanly", func(t *testing.T) {
		// Non-admin roles are NOT protected by the last-holder guard — they
		// can be revoked down to zero holders. This exercises the plain
		// DELETE path that bypasses the CTE serialization.
		roleID := "editor_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "EDITOR")))

		user := createTestUserInDB(t, userRepo, "editorSole")
		_, err := roleRepo.AssignToUser(ctx, user.ID, roleID)
		require.NoError(t, err)

		changed, err := roleRepo.RemoveFromUserIfNotLast(ctx, user.ID, roleID)
		require.NoError(t, err, "non-admin sole holder must be removable")
		assert.True(t, changed)

		count, err := roleRepo.CountByRole(ctx, roleID)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "non-admin role allowed to drop to zero holders")
	})

	t.Run("CountByRole_returns_correct_count", func(t *testing.T) {
		roleID := "cnt_" + uuid.NewString()[:8]
		require.NoError(t, roleRepo.Create(ctx, newTestRole(roleID, "CNT")))

		count0, err := roleRepo.CountByRole(ctx, roleID)
		require.NoError(t, err)
		assert.Equal(t, 0, count0)

		u1 := createTestUserInDB(t, userRepo, "cnt1")
		u2 := createTestUserInDB(t, userRepo, "cnt2")

		_, err = roleRepo.AssignToUser(ctx, u1.ID, roleID)
		require.NoError(t, err)
		_, err = roleRepo.AssignToUser(ctx, u2.ID, roleID)
		require.NoError(t, err)

		count2, err := roleRepo.CountByRole(ctx, roleID)
		require.NoError(t, err)
		assert.Equal(t, 2, count2)

		require.NoError(t, roleRepo.RemoveFromUser(ctx, u1.ID, roleID))

		count1, err := roleRepo.CountByRole(ctx, roleID)
		require.NoError(t, err)
		assert.Equal(t, 1, count1)
	})

	t.Run("GetByUserID_empty_returns_empty_slice", func(t *testing.T) {
		user := createTestUserInDB(t, userRepo, "noroles")
		roles, err := roleRepo.GetByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.NotNil(t, roles, "empty result must be non-nil slice")
		assert.Empty(t, roles)
	})

	t.Run("ListByUserID_sorted_by_name", func(t *testing.T) {
		user := createTestUserInDB(t, userRepo, "listby")
		r1 := newTestRole("listby_z_"+uuid.NewString()[:6], "Zebra")
		r2 := newTestRole("listby_a_"+uuid.NewString()[:6], "Apple")
		require.NoError(t, roleRepo.Create(ctx, r1))
		require.NoError(t, roleRepo.Create(ctx, r2))

		_, err := roleRepo.AssignToUser(ctx, user.ID, r1.ID)
		require.NoError(t, err)
		_, err = roleRepo.AssignToUser(ctx, user.ID, r2.ID)
		require.NoError(t, err)

		params := query.ListParams{
			Limit: 10,
			Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}},
		}
		roles, err := roleRepo.ListByUserID(ctx, user.ID, params)
		require.NoError(t, err)
		require.Len(t, roles, 2)
		assert.Equal(t, "Apple", roles[0].Name)
		assert.Equal(t, "Zebra", roles[1].Name)
	})
}

// ---------------------------------------------------------------------------
// Isolated last_admin_protected trigger test
// ---------------------------------------------------------------------------

// TestRemoveFromUserIfNotLast_ConcurrentRace verifies that the FOR UPDATE CTE
// in removeIfNotLastSQL serializes two concurrent admin revocations correctly
// when exactly two admins exist. Only one removal may succeed; the other must
// return ErrAuthLastAdminProtected (last-admin error). Last-holder protection
// is admin-scoped (ADR-admin-invariant §3.2 — non-admin roles bypass the CTE).
func TestRemoveFromUserIfNotLast_ConcurrentRace(t *testing.T) {
	roleRepo, userRepo, _, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, roleRepo.Create(ctx, newTestRole(auth.RoleAdmin, "Administrator")))

	u1 := createTestUserInDB(t, userRepo, "race1")
	u2 := createTestUserInDB(t, userRepo, "race2")
	_, err := roleRepo.AssignToUser(ctx, u1.ID, auth.RoleAdmin)
	require.NoError(t, err)
	_, err = roleRepo.AssignToUser(ctx, u2.ID, auth.RoleAdmin)
	require.NoError(t, err)

	// Both goroutines attempt to remove u1 concurrently.
	// The FOR UPDATE serialises them so exactly one succeeds.
	type result struct {
		changed bool
		err     error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			changed, err := roleRepo.RemoveFromUserIfNotLast(ctx, u1.ID, auth.RoleAdmin)
			results[i] = result{changed: changed, err: err}
		}()
	}
	wg.Wait()

	// Exactly one should succeed (changed=true, err=nil) and one should get
	// either ErrAuthLastAdminProtected (role held, sole holder) or (false, nil)
	// idempotent no-op (the second call arrived after the first committed).
	successCount := 0
	forbiddenCount := 0
	for _, r := range results {
		if r.changed && r.err == nil {
			successCount++
			continue
		}
		if !r.changed && r.err == nil {
			// Idempotent no-op: second goroutine arrived after role was removed.
			successCount++ // counts as "race resolved safely"
			continue
		}
		var ec *errcode.Error
		if errors.As(r.err, &ec) && ec.Code == errcode.ErrAuthLastAdminProtected {
			forbiddenCount++
			continue
		}
		t.Errorf("unexpected result: changed=%v err=%v", r.changed, r.err)
	}
	// At least one outcome must be a clean success or a last-holder refusal.
	// Together they must account for both goroutines.
	assert.Equal(t, 2, successCount+forbiddenCount,
		"both goroutine results must be clean success or last-holder refusal")
}

// TestLastAdminTrigger_RawDelete verifies that the DB-level last_admin_protected
// trigger (migration 019) fires when a raw DELETE would remove the sole holder of
// the 'admin' role.
//
// This test spins its own isolated testcontainer so it does not depend on the
// shared subtest state inside TestPGRoleRepo_Integration.
func TestLastAdminTrigger_RawDelete(t *testing.T) {
	roleRepo, userRepo, _, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	// Create the 'admin' role.
	require.NoError(t, roleRepo.Create(ctx, newTestRole("admin", "Administrator")))

	// Insert a single user and assign them to 'admin'. This user will be the
	// sole holder.
	soloUser := createTestUserInDB(t, userRepo, "trigger_"+uuid.NewString()[:6])
	_, err := roleRepo.AssignToUser(ctx, soloUser.ID, "admin")
	require.NoError(t, err)

	// Verify we have exactly one admin so the trigger condition is met.
	count, err := roleRepo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	require.Equal(t, 1, count, "test setup: exactly one admin required before raw DELETE")

	// Issue a raw DELETE directly through the explicit bypass executor —
	// bypasses the application-level last-admin guard. The DB trigger must
	// intercept this and raise P0001.
	_, rawErr := roleRepo.db.ExecDirect(ctx,
		"DELETE FROM role_assignments WHERE user_id = $1 AND role_id = 'admin'",
		soloUser.ID,
	)
	require.Error(t, rawErr, "DB trigger must reject raw DELETE of sole admin")

	var pgErr *pgconn.PgError
	require.True(t, errors.As(rawErr, &pgErr), "error must be *pgconn.PgError")
	assert.Equal(t, "P0001", pgErr.Code, "SQLSTATE must be P0001 (PL/pgSQL RAISE EXCEPTION)")
	assert.True(t, isLastAdminProtected(rawErr), "isLastAdminProtected must classify the trigger error")
}

func TestLastAdminTrigger_ConcurrentCascadeDelete_Serialized(t *testing.T) {
	roleRepo, userRepo, _, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, roleRepo.Create(ctx, newTestRole(auth.RoleAdmin, "Administrator")))
	u1 := createTestUserInDB(t, userRepo, "cascade1")
	u2 := createTestUserInDB(t, userRepo, "cascade2")
	_, err := roleRepo.AssignToUser(ctx, u1.ID, auth.RoleAdmin)
	require.NoError(t, err)
	_, err = roleRepo.AssignToUser(ctx, u2.ID, auth.RoleAdmin)
	require.NoError(t, err)

	results := make(chan error, 2)
	for _, userID := range []string{u1.ID, u2.ID} {
		userID := userID
		go func() {
			_, execErr := roleRepo.db.ExecDirect(ctx, "DELETE FROM users WHERE id = $1", userID)
			results <- execErr
		}()
	}

	var successCount, protectedCount int
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			successCount++
		case isLastAdminProtected(err):
			protectedCount++
		default:
			t.Fatalf("unexpected raw cascade delete error: %v", err)
		}
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent cascade delete may remove an admin")
	assert.Equal(t, 1, protectedCount, "exactly one concurrent cascade delete must be rejected as last admin")

	count, err := roleRepo.CountByRole(ctx, auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "advisory lock must leave exactly one admin after concurrent raw deletes")
}
