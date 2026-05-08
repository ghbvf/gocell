//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// ---------------------------------------------------------------------------
// Integration setup
// ---------------------------------------------------------------------------

// setupRoleRepoPG returns a PGRoleRepository backed by an isolated PG schema.
// It delegates container start + migration to setupPGPool (shared base container,
// one per test binary run — B1 fix).
func setupRoleRepoPG(t *testing.T) *PGRoleRepository {
	t.Helper()
	pool := setupPGPool(t)
	repo, err := NewPGRoleRepository(pool.DB(), clock.Real())
	require.NoError(t, err)
	return repo
}

// repoBundle holds the repositories, txm, and underlying pool for FK-related
// integration tests that need to execute direct SQL statements (e.g. DELETE
// FROM users, DELETE FROM roles) to trigger FK cascade/restrict behavior.
//
// txm is exposed so RemoveFromUserIfNotLast tests can wrap the call in a
// real ambient transaction — the repo's advisory-lock fail-fast guard
// rejects calls without a TxCtxKey-bearing ctx.
type repoBundle struct {
	roleRepo *PGRoleRepository
	userRepo *PGUserRepository
	txm      *adapterpg.TxManager
	pool     *pgxpool.Pool // raw pgx pool for direct SQL access in tests
}

// setupRoleRepoPGFull returns a repoBundle backed by the same isolated PG
// schema. Use this when tests need to create real users in the users table or
// execute direct SQL statements (required after migration 020 added FK on
// role_assignments).
func setupRoleRepoPGFull(t *testing.T) repoBundle {
	t.Helper()
	p := setupPGPool(t)
	roleRepo, err := NewPGRoleRepository(p.DB(), clock.Real())
	require.NoError(t, err)
	userRepo, err := NewPGUserRepository(p.DB())
	require.NoError(t, err)
	return repoBundle{
		roleRepo: roleRepo,
		userRepo: userRepo,
		txm:      adapterpg.NewTxManager(p),
		pool:     p.DB(),
	}
}

// removeIfNotLastInTx wraps RemoveFromUserIfNotLast in a TxRunner.RunInTx so
// the repo's ambient-TX guard is satisfied. Returns the (changed, err) tuple
// the repo would have produced.
func removeIfNotLastInTx(t *testing.T, b repoBundle, userID, roleID string) (bool, error) {
	t.Helper()
	var changed bool
	txErr := b.txm.RunInTx(context.Background(), func(txCtx context.Context) error {
		c, err := b.roleRepo.RemoveFromUserIfNotLast(txCtx, userID, roleID)
		changed = c
		return err
	})
	return changed, txErr
}

// newTestRole builds a minimal valid domain.Role for test insertion.
func newTestRole(name string) *domain.Role {
	return &domain.Role{
		ID:   uuid.NewString(),
		Name: name,
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	}
}

// newTestRoleNoPerms builds a domain.Role with no permissions.
func newTestRoleNoPerms(name string) *domain.Role {
	return &domain.Role{
		ID:          uuid.NewString(),
		Name:        name,
		Permissions: []domain.Permission{},
	}
}

// createTestUser inserts a user row via the given userRepo and returns its ID.
// All users created here are unique (random username+email) to avoid
// unique-constraint collisions across parallel tests.
func createTestUser(t *testing.T, ctx context.Context, userRepo *PGUserRepository) string {
	t.Helper()
	suffix := uuid.NewString()[:8]
	now := time.Now().UTC().Truncate(time.Microsecond)
	u := &domain.User{
		ID:                    uuid.NewString(),
		Username:              "user-" + suffix,
		Email:                 "user-" + suffix + "@test.example",
		PasswordHash:          "$2a$10$testhash",
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		CreationSource:        domain.UserSourceIdentity,
		CreatedAt:             now,
		UpdatedAt:             now,
		Version:               1,
	}
	require.NoError(t, userRepo.Create(ctx, u))
	return u.ID
}

// ---------------------------------------------------------------------------
// TestPGRoleRepository_Integration
// ---------------------------------------------------------------------------

func TestPGRoleRepository_Integration_Create_GetByID_HappyPath(t *testing.T) {
	repo := setupRoleRepoPG(t)
	ctx := context.Background()

	role := newTestRole("editor-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, got.ID)
	assert.Equal(t, role.Name, got.Name)
	require.Len(t, got.Permissions, 1)
	assert.Equal(t, "users", got.Permissions[0].Resource)
	assert.Equal(t, "read", got.Permissions[0].Action)
}

func TestPGRoleRepository_Integration_Create_DuplicateName_ReturnsRoleDuplicate(t *testing.T) {
	repo := setupRoleRepoPG(t)
	ctx := context.Background()

	name := "dupname-" + uuid.NewString()[:8]
	role1 := newTestRole(name)
	require.NoError(t, repo.Create(ctx, role1))

	role2 := newTestRole(name) // same name, different ID
	err := repo.Create(ctx, role2)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleDuplicate, ec.Code)
}

func TestPGRoleRepository_Integration_GetByID_NotFound_ReturnsRoleNotFound(t *testing.T) {
	repo := setupRoleRepoPG(t)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, uuid.NewString())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleNotFound, ec.Code)
}

func TestPGRoleRepository_Integration_AssignToUser_HappyPath_ReturnsChangedTrue(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("assignable-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	changed, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.True(t, changed, "first assign must return changed=true")
}

func TestPGRoleRepository_Integration_AssignToUser_Duplicate_ReturnsChangedFalse(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("dup-assign-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	changed1, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.True(t, changed1)

	changed2, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.False(t, changed2, "second assign of same role must return changed=false")
}

func TestPGRoleRepository_Integration_AssignToUser_Admin_AllowsMultipleAdmins(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	adminRole := &domain.Role{
		ID:          "admin",
		Name:        "admin",
		Permissions: []domain.Permission{},
	}
	require.NoError(t, b.roleRepo.Create(ctx, adminRole))

	user1 := createTestUser(t, ctx, b.userRepo)
	changed, err := b.roleRepo.AssignToUser(ctx, user1, "admin")
	require.NoError(t, err)
	assert.True(t, changed, "first admin assign must succeed with changed=true")

	user2 := createTestUser(t, ctx, b.userRepo)
	changed, err = b.roleRepo.AssignToUser(ctx, user2, "admin")
	require.NoError(t, err)
	assert.True(t, changed, "second admin assign must also succeed")

	count, err := b.roleRepo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, 2, count, "multiple admins must be allowed")
}

func TestPGRoleRepository_Integration_AssignToUser_Admin_5GoroutineConcurrent_AllSucceed(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	adminRole := &domain.Role{
		ID:          "admin",
		Name:        "admin-concurrent-test",
		Permissions: []domain.Permission{},
	}
	require.NoError(t, b.roleRepo.Create(ctx, adminRole))

	const N = 5
	users := make([]string, N)
	for i := range N {
		users[i] = createTestUser(t, ctx, b.userRepo)
	}

	// B5 fix: use a channel to collect per-goroutine results so that goroutine
	// writes never share a backing array slot (eliminates the theoretical race
	// detector hit on the old slice-indexed approach).
	type result struct {
		changed bool
		err     error
	}
	resultsCh := make(chan result, N)

	var wg sync.WaitGroup
	wg.Add(N)

	for i := range N {
		go func(idx int) {
			defer wg.Done()
			changed, err := b.roleRepo.AssignToUser(ctx, users[idx], "admin")
			resultsCh <- result{changed: changed, err: err}
		}(i)
	}
	wg.Wait()
	close(resultsCh)

	successCount := 0
	for r := range resultsCh {
		if r.err == nil && r.changed {
			successCount++
			continue
		}
		if r.err == nil && !r.changed {
			// ON CONFLICT DO NOTHING for same user (shouldn't happen here since all users are different)
			continue
		}
		require.NoError(t, r.err)
	}
	assert.Equal(t, N, successCount, "all concurrent admin assigns must succeed")

	count, err := b.roleRepo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, N, count, "every concurrent admin assignment must persist")
}

func TestPGRoleRepository_Integration_RemoveFromUser_HappyPath(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("removable-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	require.NoError(t, b.roleRepo.RemoveFromUser(ctx, userID, role.ID))

	// Should be idempotent (no error on second remove).
	require.NoError(t, b.roleRepo.RemoveFromUser(ctx, userID, role.ID))
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_LastHolder_ReturnsForbidden(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("sole-holder-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	// Count is 1, user is the sole holder.
	_, err = removeIfNotLastInTx(t, b, userID, role.ID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code,
		"sole holder removal must return ErrAuthForbidden")
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_NotLast_RemovesRow(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("multi-holder-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	user1 := createTestUser(t, ctx, b.userRepo)
	user2 := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, user1, role.ID)
	require.NoError(t, err)
	_, err = b.roleRepo.AssignToUser(ctx, user2, role.ID)
	require.NoError(t, err)

	// user1 is not the last holder (user2 also holds the role).
	changed, err := removeIfNotLastInTx(t, b, user1, role.ID)
	require.NoError(t, err)
	assert.True(t, changed, "removing non-last holder must return changed=true")

	// Verify count decreased.
	count, err := b.roleRepo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_UserNotHolder_ReturnsChangedFalse(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("not-assigned-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo) // created but never assigned
	changed, err := removeIfNotLastInTx(t, b, userID, role.ID)
	require.NoError(t, err)
	assert.False(t, changed, "removing role user never held must return changed=false")
}

func TestPGRoleRepository_Integration_CountByRole_HappyPath(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("counted-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	count0, err := b.roleRepo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count0)

	user1 := createTestUser(t, ctx, b.userRepo)
	user2 := createTestUser(t, ctx, b.userRepo)
	_, err = b.roleRepo.AssignToUser(ctx, user1, role.ID)
	require.NoError(t, err)
	_, err = b.roleRepo.AssignToUser(ctx, user2, role.ID)
	require.NoError(t, err)

	count2, err := b.roleRepo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, count2)
}

func TestPGRoleRepository_Integration_ListByUserID_HappyPath_ReturnsRoles(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	// Use names with known alphabetical order so the sort assertion is deterministic.
	suffix := uuid.NewString()[:8]
	role1 := newTestRoleNoPerms("aaa-viewer-" + suffix)
	role2 := newTestRoleNoPerms("bbb-editor-" + suffix)
	require.NoError(t, b.roleRepo.Create(ctx, role1))
	require.NoError(t, b.roleRepo.Create(ctx, role2))

	userID := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, userID, role1.ID)
	require.NoError(t, err)
	_, err = b.roleRepo.AssignToUser(ctx, userID, role2.ID)
	require.NoError(t, err)

	params := query.ListParams{
		Limit: 50,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}},
	}
	roles, err := b.roleRepo.ListByUserID(ctx, userID, params)
	require.NoError(t, err)
	require.Len(t, roles, 2)

	// Assert all expected role IDs are present.
	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, role1.ID)
	assert.Contains(t, ids, role2.ID)

	// Assert ascending sort by name (in-memory, matches mem repo behavior).
	assert.Equal(t, role1.Name, roles[0].Name, "first result must be alphabetically first")
	assert.Equal(t, role2.Name, roles[1].Name, "second result must be alphabetically second")
}

func TestPGRoleRepository_Integration_ListByUserID_LimitPagination_ReturnsSubset(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	// Seed 3 roles for one user; request only 2 → must return exactly 2.
	suffix := uuid.NewString()[:8]
	role1 := newTestRoleNoPerms("aaa-pg-" + suffix)
	role2 := newTestRoleNoPerms("bbb-pg-" + suffix)
	role3 := newTestRoleNoPerms("ccc-pg-" + suffix)
	require.NoError(t, b.roleRepo.Create(ctx, role1))
	require.NoError(t, b.roleRepo.Create(ctx, role2))
	require.NoError(t, b.roleRepo.Create(ctx, role3))

	userID := createTestUser(t, ctx, b.userRepo)
	for _, roleID := range []string{role1.ID, role2.ID, role3.ID} {
		_, err := b.roleRepo.AssignToUser(ctx, userID, roleID)
		require.NoError(t, err)
	}

	params := query.ListParams{
		Limit: 2,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}},
	}
	roles, err := b.roleRepo.ListByUserID(ctx, userID, params)
	require.NoError(t, err)
	// With Limit=2 and 3 items, ApplyCursor returns min(start+FetchLimit, len) =
	// min(0+3, 3) = 3 rows (N+1 detection). The caller is responsible for
	// BuildPageResult trimming. Repository contract: returns ≤ Limit+1 rows.
	assert.LessOrEqual(t, len(roles), 3, "must not return more than Limit+1 rows")
	assert.GreaterOrEqual(t, len(roles), 2, "must return at least Limit rows when data exists")
	// The first result must be alphabetically smallest.
	assert.Equal(t, role1.Name, roles[0].Name, "first result must be alphabetically first")
}

func TestPGRoleRepository_Integration_ListByUserID_EmptyUser_ReturnsEmpty(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx, b.userRepo)
	params := query.ListParams{
		Limit: 50,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}},
	}
	roles, err := b.roleRepo.ListByUserID(ctx, userID, params)
	require.NoError(t, err)
	assert.Empty(t, roles)
}

func TestPGRoleRepository_Integration_Create_NoPermissions_RoundTrip(t *testing.T) {
	repo := setupRoleRepoPG(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("empty-perms-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, got.ID)
	assert.Empty(t, got.Permissions)
}

// ---------------------------------------------------------------------------
// B6 fix: renamed from TimestampPrecision to RoundTrip; removed dead _ = before
// ---------------------------------------------------------------------------

// TestPGRoleRepository_Integration_Create_RoundTrip verifies that a newly
// created role round-trips through GetByID and returns the correct identity
// fields. The test name previously claimed timestamp-precision coverage that
// the repo does not expose (created_at is not returned by GetByID).
func TestPGRoleRepository_Integration_Create_RoundTrip(t *testing.T) {
	repo := setupRoleRepoPG(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("roundtrip-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, got.ID)
	assert.Equal(t, role.Name, got.Name)
	assert.Empty(t, got.Permissions)
}

// ---------------------------------------------------------------------------
// FK constraint tests (migration 020_role_assignments_fk.sql)
// ---------------------------------------------------------------------------

// TestPGRoleRepository_Integration_AssignToUser_NonExistentRoleID_ReturnsRoleNotFound
// verifies that assigning a non-existent roleID (FK violation on roles.id)
// returns ErrAuthRoleNotFound after migration 020 adds fk_role_assignments_role.
func TestPGRoleRepository_Integration_AssignToUser_NonExistentRoleID_ReturnsRoleNotFound(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	userID := createTestUser(t, ctx, b.userRepo)
	nonExistentRoleID := uuid.NewString()

	_, err := b.roleRepo.AssignToUser(ctx, userID, nonExistentRoleID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleNotFound, ec.Code,
		"assigning non-existent roleID must return ErrAuthRoleNotFound")
}

// TestPGRoleRepository_Integration_AssignToUser_NonExistentUserID_ReturnsUserNotFound
// verifies that assigning a role to a non-existent userID (FK violation on users.id)
// returns ErrAuthUserNotFound after migration 020 adds fk_role_assignments_user.
func TestPGRoleRepository_Integration_AssignToUser_NonExistentUserID_ReturnsUserNotFound(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	// Create a real role but use a non-existent userID.
	role := newTestRoleNoPerms("fk-user-test-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	nonExistentUserID := uuid.NewString()
	_, err := b.roleRepo.AssignToUser(ctx, nonExistentUserID, role.ID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code,
		"assigning to non-existent userID must return ErrAuthUserNotFound")
}

// TestPGRoleRepository_Integration_DeleteUser_CascadesAssignments verifies that
// deleting a user from the users table cascades and removes their role_assignments
// rows (fk_role_assignments_user ON DELETE CASCADE, migration 020).
func TestPGRoleRepository_Integration_DeleteUser_CascadesAssignments(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("cascade-test-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	// Verify assignment exists.
	count, err := b.roleRepo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "assignment must exist before user deletion")

	// Delete the user — CASCADE should remove the assignment.
	require.NoError(t, b.userRepo.Delete(ctx, userID), "deleting user must succeed")

	// The role_assignments row must be gone (CASCADE).
	count, err = b.roleRepo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count,
		"role_assignments row must be cascade-deleted when user is deleted")
}

// TestPGRoleRepository_Integration_DeleteAdminUser_AllowsReassignAdmin verifies
// that after an admin user is deleted, the admin role becomes assignable again
// (fk_role_assignments_user ON DELETE CASCADE removes the "occupation" row).
func TestPGRoleRepository_Integration_DeleteAdminUser_AllowsReassignAdmin(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	adminRole := &domain.Role{
		ID:          "admin",
		Name:        "admin",
		Permissions: []domain.Permission{},
	}
	require.NoError(t, b.roleRepo.Create(ctx, adminRole))

	// Assign first admin user.
	user1 := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, user1, "admin")
	require.NoError(t, err)

	// Delete the first admin — CASCADE removes their role_assignment row.
	require.NoError(t, b.userRepo.Delete(ctx, user1), "deleting user1 must succeed")

	// Now a second user should be able to become admin (occupation released).
	user2 := createTestUser(t, ctx, b.userRepo)
	changed, err := b.roleRepo.AssignToUser(ctx, user2, "admin")
	require.NoError(t, err)
	assert.True(t, changed,
		"after deleting the admin user, a second user must be assignable to admin")
}

// TestPGRoleRepository_Integration_DeleteRole_RestrictedWhenAssigned verifies
// that deleting a role that has active assignments is blocked by the FK
// RESTRICT constraint (fk_role_assignments_role ON DELETE RESTRICT, migration 020).
func TestPGRoleRepository_Integration_DeleteRole_RestrictedWhenAssigned(t *testing.T) {
	b := setupRoleRepoPGFull(t)
	ctx := context.Background()

	role := newTestRoleNoPerms("restrict-test-" + uuid.NewString()[:8])
	require.NoError(t, b.roleRepo.Create(ctx, role))

	userID := createTestUser(t, ctx, b.userRepo)
	_, err := b.roleRepo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	// Attempt to delete the role directly via SQL — FK RESTRICT must block this.
	_, execErr := b.pool.Exec(ctx, "DELETE FROM roles WHERE id = $1", role.ID)
	require.Error(t, execErr,
		"deleting a role with active assignments must fail (FK RESTRICT)")
	assert.Contains(t, execErr.Error(), "23503",
		"error must contain FK violation code 23503")
}
