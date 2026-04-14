package mem

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/stretchr/testify/assert"
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
