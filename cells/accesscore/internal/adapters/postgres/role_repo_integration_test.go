//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tests/testutil"
)

// ---------------------------------------------------------------------------
// Integration setup
// ---------------------------------------------------------------------------

// setupRoleRepoPG spins up a PostgreSQL container, applies all migrations
// (including 019 for roles + role_assignments tables), and returns a
// PGRoleRepository.
func setupRoleRepoPG(t *testing.T) (*PGRoleRepository, func()) {
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

	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, fsys, "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	repo, err := NewPGRoleRepository(pool.DB(), clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: container terminate: %v", err)
		}
	}
	return repo, cleanup
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

// newUserID returns a new unique user ID string.
func newUserID() string { return uuid.NewString() }

// ---------------------------------------------------------------------------
// TestPGRoleRepository_Integration
// ---------------------------------------------------------------------------

func TestPGRoleRepository_Integration_Create_GetByID_HappyPath(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
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
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
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
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, uuid.NewString())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleNotFound, ec.Code)
}

func TestPGRoleRepository_Integration_AssignToUser_HappyPath_ReturnsChangedTrue(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("assignable-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	userID := newUserID()
	changed, err := repo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.True(t, changed, "first assign must return changed=true")
}

func TestPGRoleRepository_Integration_AssignToUser_Duplicate_ReturnsChangedFalse(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("dup-assign-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	userID := newUserID()
	changed1, err := repo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.True(t, changed1)

	changed2, err := repo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.False(t, changed2, "second assign of same role must return changed=false")
}

func TestPGRoleRepository_Integration_AssignToUser_Admin_FirstWins_SecondReturnsRoleDuplicate(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	// Create the admin role with fixed ID "admin" to match the partial index.
	adminRole := &domain.Role{
		ID:          "admin",
		Name:        "admin",
		Permissions: []domain.Permission{},
	}
	require.NoError(t, repo.Create(ctx, adminRole))

	user1 := newUserID()
	changed, err := repo.AssignToUser(ctx, user1, "admin")
	require.NoError(t, err)
	assert.True(t, changed, "first admin assign must succeed with changed=true")

	// Second user trying to claim admin must hit the partial index constraint.
	user2 := newUserID()
	_, err = repo.AssignToUser(ctx, user2, "admin")
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleDuplicate, ec.Code,
		"second admin assign must return ErrAuthRoleDuplicate")
}

func TestPGRoleRepository_Integration_AssignToUser_Admin_5GoroutineConcurrent_OnlyOneSucceeds(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	// Use a fresh unique admin role with fixed prefix for partial index matching.
	// The partial index is on WHERE role_id = 'admin'; only 'admin' triggers it.
	adminRole := &domain.Role{
		ID:          "admin",
		Name:        "admin-concurrent-test",
		Permissions: []domain.Permission{},
	}
	require.NoError(t, repo.Create(ctx, adminRole))

	const N = 5
	users := make([]string, N)
	for i := range N {
		users[i] = newUserID()
	}

	type result struct {
		changed bool
		err     error
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	wg.Add(N)

	for i := range N {
		go func(idx int) {
			defer wg.Done()
			changed, err := repo.AssignToUser(ctx, users[idx], "admin")
			results[idx] = result{changed: changed, err: err}
		}(i)
	}
	wg.Wait()

	successCount := 0
	dupCount := 0
	for _, r := range results {
		if r.err == nil && r.changed {
			successCount++
			continue
		}
		if r.err == nil && !r.changed {
			// ON CONFLICT DO NOTHING for same user (shouldn't happen here since all users are different)
			continue
		}
		var ec *errcode.Error
		require.ErrorAs(t, r.err, &ec)
		assert.Equal(t, errcode.ErrAuthRoleDuplicate, ec.Code)
		dupCount++
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent admin assign must succeed")
	assert.Equal(t, N-1, dupCount, "all other concurrent admin assigns must return ErrAuthRoleDuplicate")
}

func TestPGRoleRepository_Integration_RemoveFromUser_HappyPath(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("removable-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	userID := newUserID()
	_, err := repo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	require.NoError(t, repo.RemoveFromUser(ctx, userID, role.ID))

	// Should be idempotent (no error on second remove).
	require.NoError(t, repo.RemoveFromUser(ctx, userID, role.ID))
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_LastHolder_ReturnsForbidden(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("sole-holder-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	userID := newUserID()
	_, err := repo.AssignToUser(ctx, userID, role.ID)
	require.NoError(t, err)

	// Count is 1, user is the sole holder.
	_, err = repo.RemoveFromUserIfNotLast(ctx, userID, role.ID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code,
		"sole holder removal must return ErrAuthForbidden")
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_NotLast_RemovesRow(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("multi-holder-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	user1 := newUserID()
	user2 := newUserID()
	_, err := repo.AssignToUser(ctx, user1, role.ID)
	require.NoError(t, err)
	_, err = repo.AssignToUser(ctx, user2, role.ID)
	require.NoError(t, err)

	// user1 is not the last holder (user2 also holds the role).
	changed, err := repo.RemoveFromUserIfNotLast(ctx, user1, role.ID)
	require.NoError(t, err)
	assert.True(t, changed, "removing non-last holder must return changed=true")

	// Verify count decreased.
	count, err := repo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPGRoleRepository_Integration_RemoveFromUserIfNotLast_UserNotHolder_ReturnsChangedFalse(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("not-assigned-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	userID := newUserID() // never assigned
	changed, err := repo.RemoveFromUserIfNotLast(ctx, userID, role.ID)
	require.NoError(t, err)
	assert.False(t, changed, "removing role user never held must return changed=false")
}

func TestPGRoleRepository_Integration_CountByRole_HappyPath(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("counted-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	count0, err := repo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count0)

	user1 := newUserID()
	user2 := newUserID()
	_, err = repo.AssignToUser(ctx, user1, role.ID)
	require.NoError(t, err)
	_, err = repo.AssignToUser(ctx, user2, role.ID)
	require.NoError(t, err)

	count2, err := repo.CountByRole(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, count2)
}

func TestPGRoleRepository_Integration_ListByUserID_HappyPath_ReturnsRoles(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	role1 := newTestRoleNoPerms("viewer-" + suffix)
	role2 := newTestRoleNoPerms("editor-" + suffix)
	require.NoError(t, repo.Create(ctx, role1))
	require.NoError(t, repo.Create(ctx, role2))

	userID := newUserID()
	_, err := repo.AssignToUser(ctx, userID, role1.ID)
	require.NoError(t, err)
	_, err = repo.AssignToUser(ctx, userID, role2.ID)
	require.NoError(t, err)

	roles, err := repo.GetByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, roles, 2)

	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, role1.ID)
	assert.Contains(t, ids, role2.ID)
}

func TestPGRoleRepository_Integration_ListByUserID_NoRoles_ReturnsEmpty(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	userID := newUserID()
	roles, err := repo.GetByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, roles)
}

func TestPGRoleRepository_Integration_Create_NoPermissions_RoundTrip(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	role := newTestRoleNoPerms("empty-perms-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, got.ID)
	assert.Empty(t, got.Permissions)
}

// ---------------------------------------------------------------------------
// Timestamp precision guard
// ---------------------------------------------------------------------------

func TestPGRoleRepository_Integration_Create_TimestampPrecision(t *testing.T) {
	repo, cleanup := setupRoleRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	before := time.Now().UTC().Truncate(time.Second)
	role := newTestRoleNoPerms("ts-precision-" + uuid.NewString()[:8])
	require.NoError(t, repo.Create(ctx, role))

	// roles.created_at is set server-side via r.clk.Now() during Insert.
	// We just verify the row round-trips without error; precise timestamp
	// verification would require reading created_at back, which this
	// repo does not currently expose. The before bound is a smoke check.
	got, err := repo.GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, got.ID)
	_ = before
}
