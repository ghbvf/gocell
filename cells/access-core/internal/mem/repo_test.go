package mem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUserRepository_ConcurrentCreateAndGet verifies that concurrent
// Create and Get calls do not race. Run with -race to verify.
func TestUserRepository_ConcurrentCreateAndGet(t *testing.T) {
	repo := NewUserRepository()
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
				_ = repo.Create(ctx, &domain.User{
					ID:       fmt.Sprintf("uid-w%d-i%d", id, i),
					Username: fmt.Sprintf("user-w%d-i%d", id, i),
					Email:    fmt.Sprintf("u%d-%d@test.com", id, i),
					Status:   domain.StatusActive,
				})
			}
		}(w)
	}

	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = repo.GetByID(ctx, "uid-w0-i0")
				_, _ = repo.GetByUsername(ctx, "user-w0-i0")
			}
			_ = r
		}()
	}

	wg.Wait()
}

func TestSessionRepository_Health(t *testing.T) {
	repo := NewSessionRepository()
	assert.NoError(t, repo.Health(), "in-memory session repo is always healthy")
}

// TestSessionRepository_ConcurrentCreateAndGet verifies that concurrent
// Create and Get calls do not race. Run with -race to verify.
func TestSessionRepository_ConcurrentCreateAndGet(t *testing.T) {
	repo := NewSessionRepository()
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
				_ = repo.Create(ctx, &domain.Session{
					ID:           fmt.Sprintf("sid-w%d-i%d", id, i),
					UserID:       fmt.Sprintf("uid-%d", id),
					RefreshToken: fmt.Sprintf("rt-w%d-i%d", id, i),
					ExpiresAt:    time.Now().Add(time.Hour),
					CreatedAt:    time.Now(),
				})
			}
		}(w)
	}

	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = repo.GetByID(ctx, "sid-w0-i0")
				_, _ = repo.GetByRefreshToken(ctx, "rt-w0-i0")
			}
			_ = r
		}()
	}

	wg.Wait()
}

// TestRoleRepository_ConcurrentAssignAndGet verifies that concurrent
// Assign and Get calls do not race. Run with -race to verify.
func TestRoleRepository_ConcurrentAssignAndGet(t *testing.T) {
	repo := NewRoleRepository()
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
				_ = repo.AssignToUser(ctx, userID, fmt.Sprintf("role-%d", id%5))
			}
		}(w)
	}

	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = repo.GetByID(ctx, "role-0")
				_, _ = repo.GetByUserID(ctx, "uid-w0-i0")
			}
			_ = r
		}()
	}

	wg.Wait()
	assert.NotNil(t, repo) // ensure repo survived concurrent access
}

// TestRoleRepository_ConcurrentRemoveFromUserIfNotLast verifies that when
// multiple goroutines concurrently try to revoke the role from the only
// remaining holders, exactly one admin is preserved. Run with -race to
// verify the atomic count+delete under write lock.
func TestRoleRepository_ConcurrentRemoveFromUserIfNotLast(t *testing.T) {
	repo := NewRoleRepository()
	ctx := context.Background()
	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	// Seed N admin holders, then launch N goroutines each trying to revoke
	// its own admin role. The atomic guard must keep at least one holder.
	const holders = 8
	for i := range holders {
		require.NoError(t, repo.AssignToUser(ctx, fmt.Sprintf("uid-%d", i), "admin"))
	}

	var wg sync.WaitGroup
	errs := make(chan error, holders)
	for i := range holders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs <- repo.RemoveFromUserIfNotLast(ctx, fmt.Sprintf("uid-%d", idx), "admin")
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
		if errors.As(err, &ecErr) && ecErr.Code == errcode.ErrAuthForbidden {
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

func TestRoleRepository_Create(t *testing.T) {
	repo := NewRoleRepository()
	ctx := context.Background()

	role := &domain.Role{ID: "editor", Name: "editor", Permissions: []domain.Permission{{Resource: "docs", Action: "write"}}}
	require.NoError(t, repo.Create(ctx, role))

	got, err := repo.GetByID(ctx, "editor")
	require.NoError(t, err)
	assert.Equal(t, "editor", got.ID)
	assert.Len(t, got.Permissions, 1)
}

func TestRoleRepository_Create_Idempotent(t *testing.T) {
	repo := NewRoleRepository()
	ctx := context.Background()

	role := &domain.Role{ID: "admin", Name: "admin"}
	require.NoError(t, repo.Create(ctx, role))
	require.NoError(t, repo.Create(ctx, role)) // second call is no-op

	got, err := repo.GetByID(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", got.ID)
}

func TestRoleRepository_CountByRole(t *testing.T) {
	repo := NewRoleRepository()
	ctx := context.Background()

	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	require.NoError(t, repo.AssignToUser(ctx, "usr-1", "admin"))
	require.NoError(t, repo.AssignToUser(ctx, "usr-2", "admin"))

	count, err := repo.CountByRole(ctx, "admin")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestRoleRepository_CountByRole_None(t *testing.T) {
	repo := NewRoleRepository()
	ctx := context.Background()

	count, err := repo.CountByRole(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestSessionRepository_Update_VersionConflict verifies that updating a session
// with a stale version returns ErrSessionConflict.
func TestSessionRepository_Update_VersionConflict(t *testing.T) {
	repo := NewSessionRepository()
	ctx := context.Background()

	sess := &domain.Session{
		ID:           "sess-vc",
		UserID:       "usr-1",
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		Version:      1,
	}
	require.NoError(t, repo.Create(ctx, sess))

	// Read twice — simulating two concurrent goroutines.
	clone1, err := repo.GetByRefreshToken(ctx, "rt-1")
	require.NoError(t, err)
	clone2, err := repo.GetByRefreshToken(ctx, "rt-1")
	require.NoError(t, err)

	// First update succeeds.
	clone1.RefreshToken = "rt-2"
	clone1.PreviousRefreshToken = "rt-1"
	require.NoError(t, repo.Update(ctx, clone1))

	// Second update with stale version should fail.
	clone2.RefreshToken = "rt-3"
	clone2.PreviousRefreshToken = "rt-1"
	err = repo.Update(ctx, clone2)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrSessionConflict, ecErr.Code)
}

// TestSessionRepository_Update_VersionIncrement verifies that version is
// incremented on each successful update.
func TestSessionRepository_Update_VersionIncrement(t *testing.T) {
	repo := NewSessionRepository()
	ctx := context.Background()

	sess := &domain.Session{
		ID:           "sess-vi",
		UserID:       "usr-1",
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		Version:      1,
	}
	require.NoError(t, repo.Create(ctx, sess))

	for i := 1; i <= 3; i++ {
		s, err := repo.GetByID(ctx, "sess-vi")
		require.NoError(t, err)
		assert.Equal(t, int64(i), s.Version)

		s.RefreshToken = fmt.Sprintf("rt-%d", i+1)
		s.PreviousRefreshToken = fmt.Sprintf("rt-%d", i)
		require.NoError(t, repo.Update(ctx, s))
	}

	final, err := repo.GetByID(ctx, "sess-vi")
	require.NoError(t, err)
	assert.Equal(t, int64(4), final.Version)
}

// TestSessionRepository_ConcurrentRefreshUpdate verifies that concurrent
// updates to the same session result in exactly one success and the rest
// returning ErrSessionConflict. Run with -race.
func TestSessionRepository_ConcurrentRefreshUpdate(t *testing.T) {
	repo := NewSessionRepository()
	ctx := context.Background()

	sess := &domain.Session{
		ID:           "sess-cru",
		UserID:       "usr-1",
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		Version:      1,
	}
	require.NoError(t, repo.Create(ctx, sess))

	const goroutines = 10
	var (
		wg        sync.WaitGroup
		successes int64
		conflicts int64
		mu        sync.Mutex
	)

	// All goroutines read the same version, then try to update.
	clones := make([]*domain.Session, goroutines)
	for i := range goroutines {
		clone, err := repo.GetByRefreshToken(ctx, "rt-1")
		require.NoError(t, err)
		clone.RefreshToken = fmt.Sprintf("rt-new-%d", i)
		clone.PreviousRefreshToken = "rt-1"
		clones[i] = clone
	}

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := repo.Update(ctx, clones[idx])
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				var ecErr *errcode.Error
				if assert.ErrorAs(t, err, &ecErr) {
					assert.Equal(t, errcode.ErrSessionConflict, ecErr.Code)
				}
				conflicts++
			}
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int64(1), successes, "exactly one goroutine should succeed")
	assert.Equal(t, int64(goroutines-1), conflicts, "all others should get version conflict")
}

// TestSessionRepository_Create_SetsVersion verifies that Create initializes
// Version to 1 even if the caller passes 0.
func TestSessionRepository_Create_SetsVersion(t *testing.T) {
	repo := NewSessionRepository()
	ctx := context.Background()

	sess := &domain.Session{
		ID:           "sess-cv",
		UserID:       "usr-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		// Version intentionally omitted (zero value)
	}
	require.NoError(t, repo.Create(ctx, sess))

	stored, err := repo.GetByID(ctx, "sess-cv")
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Version, "Version should be initialized to 1")
}
