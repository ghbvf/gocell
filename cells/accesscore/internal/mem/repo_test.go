package mem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// TestUserRepository_ConcurrentCreateAndGet verifies that concurrent
// Create and Get calls do not race. Run with -race to verify.
func TestUserRepository_ConcurrentCreateAndGet(t *testing.T) {
	repo := NewStore(clock.Real()).UserRepository()
	ctx := context.Background()

	const writers = 5
	const readers = 10
	const iterations = 50

	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				u, _ := domain.NewUser(
					fmt.Sprintf("user-w%d-i%d", id, i),
					fmt.Sprintf("u%d-%d@test.com", id, i),
					"$2a$12$hash",
					time.Now(),
				)
				if u != nil {
					u.ID = fmt.Sprintf("uid-w%d-i%d", id, i)
					_ = repo.Create(ctx, u)
				}
			}
		}(w)
	}

	for r := range readers {
		wg.Go(func() {
			for range iterations {
				_, _ = repo.GetByID(ctx, "uid-w0-i0")
				_, _ = repo.GetByUsername(ctx, "user-w0-i0")
			}
			_ = r
		})
	}

	wg.Wait()
}

func TestUserRepository_NotFoundErrors(t *testing.T) {
	repo := NewStore(clock.Real()).UserRepository()
	ctx := context.Background()

	tests := []struct {
		name         string
		call         func() error
		wantCode     errcode.Code
		wantInternal string
	}{
		{
			name: "get by id",
			call: func() error {
				_, err := repo.GetByID(ctx, "usr-missing")
				return err
			},
			wantCode:     errcode.ErrAuthUserNotFound,
			wantInternal: "id=usr-missing",
		},
		{
			name: "get by username",
			call: func() error {
				_, err := repo.GetByUsername(ctx, "missing")
				return err
			},
			wantCode:     errcode.ErrAuthUserNotFound,
			wantInternal: `username="missing"`,
		},
		{
			name: "update",
			call: func() error {
				// Build via NewUser+ID injection (struct literal not allowed outside package).
				u, _ := domain.NewUser("missing", "missing@test.local", "$2a$12$hash", time.Now())
				if u == nil {
					return nil
				}
				u.ID = "usr-missing"
				return repo.Update(ctx, u)
			},
			wantCode:     errcode.ErrAuthUserNotFound,
			wantInternal: "id=usr-missing",
		},
		{
			name: "delete",
			call: func() error {
				return repo.Delete(ctx, "usr-missing")
			},
			wantCode:     errcode.ErrAuthUserNotFound,
			wantInternal: "id=usr-missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, tt.wantCode, ecErr.Code)
			assert.Equal(t, msgUserNotFound, ecErr.Message)
			assert.Contains(t, ecErr.InternalMessage, tt.wantInternal)
		})
	}
}

// TestRoleRepository_ConcurrentAssignAndGet verifies that concurrent
// Assign and Get calls do not race. Run with -race to verify.
func TestRoleRepository_ConcurrentAssignAndGet(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	// Seed roles.
	for i := range 5 {
		repo.SeedRole(&domain.Role{
			ID:   fmt.Sprintf("role-%d", i),
			Name: fmt.Sprintf("Role %d", i),
		})
	}

	const writers = 5
	const readers = 10
	const iterations = 50

	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				userID := fmt.Sprintf("uid-w%d-i%d", id, i)
				_, _ = repo.AssignToUser(ctx, userID, fmt.Sprintf("role-%d", id%5))
			}
		}(w)
	}

	for r := range readers {
		wg.Go(func() {
			for range iterations {
				_, _ = repo.GetByID(ctx, "role-0")
				_, _ = repo.GetByUserID(ctx, "uid-w0-i0")
			}
			_ = r
		})
	}

	wg.Wait()
	assert.NotNil(t, repo) // ensure repo survived concurrent access
}

// TestRoleRepository_ConcurrentRemoveFromUserIfNotLast verifies that when
// multiple goroutines concurrently try to revoke the role from the only
// remaining holders, exactly one effective admin is preserved (S4.0: holders
// must be status='active' to count). Run with -race to verify the atomic
// count+delete under the shared store write lock.
func TestRoleRepository_ConcurrentRemoveFromUserIfNotLast(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.RoleRepository()
	userRepo := store.UserRepository()
	ctx := context.Background()
	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	// Seed N active users with admin role. Effective-admin semantics require
	// the users to exist in usersByID with Status=active, otherwise the
	// invariant counter would return 0 and every revoke would be rejected.
	const holders = 8
	for i := range holders {
		id := fmt.Sprintf("uid-%d", i)
		hu, huErr := domain.NewUser(id, id+"@test.local", "$2a$12$hash", time.Now())
		require.NoError(t, huErr)
		hu.ID = id
		require.NoError(t, userRepo.Create(ctx, hu))
		_, assignErr := repo.AssignToUser(ctx, id, "admin")
		require.NoError(t, assignErr)
	}

	var wg sync.WaitGroup
	errs := make(chan error, holders)
	for i := range holders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := repo.RemoveFromUserIfNotLast(ctx, fmt.Sprintf("uid-%d", idx), "admin")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)

	// Count successes and last-holder rejections.
	var success, rejected int
	for err := range errs {
		if err == nil {
			success++
			continue
		}
		var ecErr *errcode.Error
		if errors.As(err, &ecErr) && ecErr.Code == errcode.ErrAuthLastAdminProtected {
			rejected++
			continue
		}
		t.Fatalf("unexpected error: %v", err)
	}

	// Exactly one holder must remain: success = holders-1, rejected = 1.
	assert.Equal(t, holders-1, success, "all but the last holder should be removable")
	assert.Equal(t, 1, rejected, "exactly one revoke must be rejected by last-holder guard")

	count, err := repo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "exactly one admin holder must survive concurrent revokes")
}

// TestRoleRepository_RemoveFromUserIfNotLast_NonAdminScopeNotProtected verifies
// that the last-holder guard is admin-scoped (ADR-admin-invariant §3.2). For
// non-admin roles, RemoveFromUserIfNotLast must allow revocation down to zero
// holders — otherwise transient high-privilege roles cannot be reclaimed and
// leak forever.
func TestRoleRepository_RemoveFromUserIfNotLast_NonAdminScopeNotProtected(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()
	repo.SeedRole(&domain.Role{ID: "editor", Name: "editor"})

	_, err := repo.AssignToUser(ctx, "u1", "editor")
	require.NoError(t, err)

	// Sole holder of a non-admin role MUST be removable (count drops to 0).
	changed, err := repo.RemoveFromUserIfNotLast(ctx, "u1", "editor")
	require.NoError(t, err, "non-admin sole holder must be revocable")
	assert.True(t, changed, "non-admin removal must report state change")

	count, err := repo.CountByRole(ctx, "editor")
	require.NoError(t, err)
	assert.Equal(t, 0, count, "non-admin role must drop to zero holders")
}

// TestRoleRepository_GetByUserID_NoRoles verifies that a user with no role
// assignments returns a non-nil empty slice, not nil. This is a hygiene guard
// against future callers that serialize the result directly (nil → JSON null).
func TestRoleRepository_GetByUserID_NoRoles(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	roles, err := repo.GetByUserID(ctx, "user-with-no-roles")
	require.NoError(t, err)
	require.NotNil(t, roles, "empty result must be non-nil slice")
	require.Empty(t, roles)
}

func TestRoleRepository_ListByUserID_NoRoles(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()

	roles, err := repo.ListByUserID(context.Background(), "user-with-no-roles", query.ListParams{
		Limit: 2,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}},
	})

	require.NoError(t, err)
	require.NotNil(t, roles, "empty result must be non-nil slice")
	require.Empty(t, roles)
}

func TestRoleRepository_ListByUserID_SortsPagesAndClones(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()
	roles := []*domain.Role{
		{ID: "role-c", Name: "viewer", Permissions: []domain.Permission{{Resource: "devices", Action: "read"}}},
		{ID: "role-a", Name: "admin", Permissions: []domain.Permission{{Resource: "users", Action: "write"}}},
		{ID: "role-b", Name: "operator", Permissions: []domain.Permission{{Resource: "devices", Action: "write"}}},
	}
	for _, role := range roles {
		repo.SeedRole(role)
		_, err := repo.AssignToUser(ctx, "user-1", role.ID)
		require.NoError(t, err)
	}

	params := query.ListParams{
		Limit: 2,
		Sort: []query.SortColumn{
			{Name: "name", Direction: query.SortASC},
			{Name: "id", Direction: query.SortASC},
		},
	}
	page, err := repo.ListByUserID(ctx, "user-1", params)
	require.NoError(t, err)
	require.Len(t, page, 3, "repository returns Limit+1 rows for page-result hasMore detection")
	assert.Equal(t, []string{"admin", "operator", "viewer"}, []string{page[0].Name, page[1].Name, page[2].Name})

	page[0].Permissions[0].Action = "mutated"
	got, err := repo.GetByID(ctx, "role-a")
	require.NoError(t, err)
	assert.Equal(t, "write", got.Permissions[0].Action, "listed roles must be cloned from repository state")

	params.CursorValues = []any{"operator", "role-b"}
	nextPage, err := repo.ListByUserID(ctx, "user-1", params)
	require.NoError(t, err)
	require.Len(t, nextPage, 1)
	assert.Equal(t, "viewer", nextPage[0].Name)
}

func TestRoleRepository_ListByUserID_InvalidCursorParams(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()
	repo.SeedRole(&domain.Role{ID: "role-a", Name: "admin"})
	_, err := repo.AssignToUser(ctx, "user-1", "role-a")
	require.NoError(t, err)

	_, err = repo.ListByUserID(ctx, "user-1", query.ListParams{
		Limit:        2,
		Sort:         []query.SortColumn{{Name: "name", Direction: query.SortASC}},
		CursorValues: []any{"admin", "role-a"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "role-repo: list-by-user")
}

func TestRoleRepository_Create(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	role := &domain.Role{ID: "editor", Name: "editor", Permissions: []domain.Permission{{Resource: "docs", Action: "write"}}}
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, "editor")
	require.NoError(t, err)
	assert.Equal(t, "editor", got.ID)
	assert.Len(t, got.Permissions, 1)
}

func TestRoleRepository_Create_Idempotent(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	role := &domain.Role{ID: "admin", Name: "admin"}
	require.NoError(t, repo.Create(ctx, role))
	require.NoError(t, repo.Create(ctx, role)) // second call is no-op

	got, err := repo.GetByID(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", got.ID)
}

func TestRoleRepository_CountByRole(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	_, err := repo.AssignToUser(ctx, "usr-1", "admin")
	require.NoError(t, err)
	_, err = repo.AssignToUser(ctx, "usr-2", "admin")
	require.NoError(t, err)

	count, err := repo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestRoleRepository_CountByRole_None(t *testing.T) {
	repo := NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	count, err := repo.CountByRole(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// --- S4.0 effective-admin invariant tests ---------------------------------

// seedActiveAdmin creates a status='active' user and assigns the admin role
// inside a single shared Store so the user is an effective admin.
func seedActiveAdmin(t testing.TB, store *Store, userID string) {
	t.Helper()
	u, err := domain.NewUser(userID, userID+"@test.local", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u.ID = userID
	require.NoError(t, store.UserRepository().Create(context.Background(), u))
	_, err = store.RoleRepository().AssignToUser(context.Background(), userID, "admin")
	require.NoError(t, err)
}

// TestRoleRepository_EffectiveAdminExists covers the lock-free fast-path
// boolean variant used by provisioner.Status / setup-retirement gating.
// Mirrors the CountEffectiveAdmins filter (status='active' AND admin role)
// but returns bool — verify both polarities.
func TestRoleRepository_EffectiveAdminExists(t *testing.T) {
	t.Run("empty_store_returns_false", func(t *testing.T) {
		store := NewStore(clock.Real())
		exists, err := store.RoleRepository().EffectiveAdminExists(context.Background())
		require.NoError(t, err)
		assert.False(t, exists, "fresh store has no effective admin")
	})

	t.Run("only_locked_admin_returns_false", func(t *testing.T) {
		// Seed a user already in locked state + admin role. Going through
		// UserRepository.Update would trip the effective-admin guard, so
		// we write directly into the store maps (this test lives in
		// package mem and exercises the read-side predicate).
		store := NewStore(clock.Real())
		store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
		now := time.Now()
		lockedUser, err := domain.ReconstituteUser(
			"locked-admin", "locked-admin", "la@test.local", "$2a$12$hash",
			0, false, domain.StatusLocked, domain.UserSourceIdentity, 1, now, now,
		)
		require.NoError(t, err)
		store.mu.Lock()
		store.usersByID["locked-admin"] = lockedUser
		store.byName["locked-admin"] = lockedUser
		store.userRoles["locked-admin"] = map[string]struct{}{"admin": {}}
		store.mu.Unlock()

		exists, err := store.RoleRepository().EffectiveAdminExists(context.Background())
		require.NoError(t, err)
		assert.False(t, exists, "locked admin must not satisfy the effective predicate")
	})

	t.Run("active_admin_returns_true", func(t *testing.T) {
		store := NewStore(clock.Real())
		store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
		seedActiveAdmin(t, store, "active-admin")

		exists, err := store.RoleRepository().EffectiveAdminExists(context.Background())
		require.NoError(t, err)
		assert.True(t, exists, "active admin must satisfy the effective predicate")
	})

	t.Run("orphan_role_assignment_returns_false", func(t *testing.T) {
		// Role assignment exists for a userID that has no users row (FK CASCADE
		// would prevent this in PG, but the mem path defensively skips orphans).
		store := NewStore(clock.Real())
		store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
		_, err := store.RoleRepository().AssignToUser(context.Background(), "ghost", "admin")
		require.NoError(t, err)

		exists, err := store.RoleRepository().EffectiveAdminExists(context.Background())
		require.NoError(t, err)
		assert.False(t, exists, "orphan role assignment without user row must not count")
	})

	t.Run("non_admin_role_only_returns_false", func(t *testing.T) {
		// Active user holds a non-admin role only; effective-admin predicate
		// must reject because the role-id filter ignores them.
		store := NewStore(clock.Real())
		store.RoleRepository().SeedRole(&domain.Role{ID: "viewer", Name: "viewer"})
		vu, vuErr := domain.NewUser("viewer-user", "vu@test.local", "$2a$12$hash", time.Now())
		require.NoError(t, vuErr)
		vu.ID = "viewer-user"
		require.NoError(t, store.UserRepository().Create(context.Background(), vu))
		_, assignErr := store.RoleRepository().AssignToUser(context.Background(), "viewer-user", "viewer")
		require.NoError(t, assignErr)

		exists, err := store.RoleRepository().EffectiveAdminExists(context.Background())
		require.NoError(t, err)
		assert.False(t, exists, "non-admin role must not satisfy effective-admin predicate")
	})
}

// TestRoleRepository_CountEffectiveAdmins_FiltersLocked covers the core
// S4.0 invariant counter: a user with admin role but non-active status does
// NOT count toward the at-least-one-effective-admin invariant.
func TestRoleRepository_CountEffectiveAdmins_FiltersLocked(t *testing.T) {
	store := NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	seedActiveAdmin(t, store, "active-admin")
	seedActiveAdmin(t, store, "locked-admin")
	// Lock one admin — now only one effective admin remains.
	u, err := store.UserRepository().GetByID(context.Background(), "locked-admin")
	require.NoError(t, err)
	u.SetStatus(domain.StatusLocked, time.Now())
	require.NoError(t, store.UserRepository().Update(context.Background(), u))

	count, err := store.RoleRepository().CountEffectiveAdmins(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count, "locked admin must not be counted")

	// CountByRole sees BOTH (it's the bootstrap-idempotency counter, status-blind).
	rawCount, err := store.RoleRepository().CountByRole(context.Background(), "admin")
	require.NoError(t, err)
	assert.Equal(t, 2, rawCount, "CountByRole is status-blind by design")
}

// TestRoleRepository_RemoveFromUserIfNotLast_LockedPeerDoesNotCount asserts
// that an active admin cannot be revoked when the only other admin is
// locked — the locked peer is not a usable fallback.
func TestRoleRepository_RemoveFromUserIfNotLast_LockedPeerDoesNotCount(t *testing.T) {
	store := NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	seedActiveAdmin(t, store, "active-admin")
	seedActiveAdmin(t, store, "locked-admin")
	u, err := store.UserRepository().GetByID(context.Background(), "locked-admin")
	require.NoError(t, err)
	u.SetStatus(domain.StatusLocked, time.Now())
	require.NoError(t, store.UserRepository().Update(context.Background(), u))

	changed, err := store.RoleRepository().RemoveFromUserIfNotLast(context.Background(), "active-admin", "admin")
	require.Error(t, err, "must refuse revoke when only peer is locked")
	assert.False(t, changed)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrAuthLastAdminProtected, ecErr.Code)
}

// TestRoleRepository_RemoveFromUserIfNotLast_LockedAdminCanBeRevoked is the
// inverse: revoking a locked admin's role does not reduce the effective-admin
// count (locked is already excluded), so it must succeed even with only one
// active peer.
func TestRoleRepository_RemoveFromUserIfNotLast_LockedAdminCanBeRevoked(t *testing.T) {
	store := NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	seedActiveAdmin(t, store, "active-admin")
	seedActiveAdmin(t, store, "locked-admin")
	u, err := store.UserRepository().GetByID(context.Background(), "locked-admin")
	require.NoError(t, err)
	u.SetStatus(domain.StatusLocked, time.Now())
	require.NoError(t, store.UserRepository().Update(context.Background(), u))

	changed, err := store.RoleRepository().RemoveFromUserIfNotLast(context.Background(), "locked-admin", "admin")
	require.NoError(t, err)
	assert.True(t, changed, "locked admin revoke does not reduce effective-admin count")
}

// TestStore_SharedMutex_AtomicityAcrossRepos validates the cross-repo
// atomicity guarantee that the S4.0 plan promised: concurrent UserRepository
// status mutation and RoleRepository admin revoke serialize through the
// shared mutex, mirroring the PG advisory-lock + FOR UPDATE serialization.
func TestStore_SharedMutex_AtomicityAcrossRepos(t *testing.T) {
	store := NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	seedActiveAdmin(t, store, "admin-a")
	seedActiveAdmin(t, store, "admin-b")

	// Goroutine A locks admin-b. Goroutine B revokes admin role from admin-a.
	// Without shared mutex, both could observe the other as still effective
	// and succeed — leaving zero effective admins. With shared mutex one of
	// them must lose (the role revoke sees admin-b as locked, or the lock
	// sees admin-a's admin role revoked).
	var wg sync.WaitGroup
	wg.Add(2)
	var revokeErr error
	go func() {
		defer wg.Done()
		u, err := store.UserRepository().GetByID(context.Background(), "admin-b")
		if err == nil {
			u.SetStatus(domain.StatusLocked, time.Now())
			_ = store.UserRepository().Update(context.Background(), u)
		}
	}()
	go func() {
		defer wg.Done()
		_, revokeErr = store.RoleRepository().RemoveFromUserIfNotLast(context.Background(), "admin-a", "admin")
	}()
	wg.Wait()

	// Final state: at least one effective admin must remain. Either revokeErr
	// is non-nil (refused because admin-b was locked first) or admin-a still
	// holds admin (because admin-b was still active when revoke ran).
	count, err := store.RoleRepository().CountEffectiveAdmins(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 1, "shared mutex must keep at least one effective admin")
	_ = revokeErr // either nil or ErrAuthLastAdminProtected, both valid orderings
}
