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

// setupUserRepoPG spins up a PostgreSQL container, applies all migrations
// (including 017 for the users table), and returns a PGUserRepository.
func setupUserRepoPG(t *testing.T) (*PGUserRepository, func()) {
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

	repo, err := NewPGUserRepository(pool.DB(), clock.Real())
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

// newTestUser builds a minimal valid domain.User for test insertion.
func newTestUser(username, email string) *domain.User {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.User{
		ID:                    uuid.NewString(),
		Username:              username,
		Email:                 email,
		PasswordHash:          "$2a$10$testhash",
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		CreationSource:        domain.UserSourceIdentity,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

// ---------------------------------------------------------------------------
// TestPGUserRepository_Integration_CRUD
// ---------------------------------------------------------------------------

func TestPGUserRepository_Integration_CRUD(t *testing.T) {
	repo, cleanup := setupUserRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Create_and_GetByID", func(t *testing.T) {
		u := newTestUser("alice", "alice@example.com")
		require.NoError(t, repo.Create(ctx, u))

		got, err := repo.GetByID(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
		assert.Equal(t, u.Email, got.Email)
		assert.Equal(t, u.PasswordHash, got.PasswordHash)
		assert.Equal(t, u.Status, got.Status)
		assert.Equal(t, u.CreationSource, got.CreationSource)
		assert.Equal(t, u.PasswordResetRequired, got.PasswordResetRequired)
	})

	t.Run("Create_and_GetByUsername", func(t *testing.T) {
		u := newTestUser("bob", "bob@example.com")
		require.NoError(t, repo.Create(ctx, u))

		got, err := repo.GetByUsername(ctx, "bob")
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, "bob", got.Username)
	})

	t.Run("Update", func(t *testing.T) {
		u := newTestUser("carol", "carol@example.com")
		require.NoError(t, repo.Create(ctx, u))

		u.Email = "carol-updated@example.com"
		u.PasswordResetRequired = true
		u.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
		require.NoError(t, repo.Update(ctx, u))

		got, err := repo.GetByID(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, "carol-updated@example.com", got.Email)
		assert.True(t, got.PasswordResetRequired)
	})

	t.Run("Delete", func(t *testing.T) {
		u := newTestUser("dave", "dave@example.com")
		require.NoError(t, repo.Create(ctx, u))

		require.NoError(t, repo.Delete(ctx, u.ID))

		_, err := repo.GetByID(ctx, u.ID)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("GetByID_NotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("GetByUsername_NotFound", func(t *testing.T) {
		_, err := repo.GetByUsername(ctx, "nonexistent-user-xyz")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Update_NotFound", func(t *testing.T) {
		ghost := newTestUser("ghost", "ghost@example.com")
		ghost.ID = uuid.NewString() // not in DB

		err := repo.Update(ctx, ghost)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Delete_NotFound", func(t *testing.T) {
		err := repo.Delete(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Create_DuplicateUsername_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u1 := newTestUser("dupuser", "dup1@example.com")
		require.NoError(t, repo.Create(ctx, u1))

		u2 := newTestUser("dupuser", "dup2@example.com")
		err := repo.Create(ctx, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
	})

	t.Run("Create_DuplicateEmail_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u1 := newTestUser("emaildup1", "sharedemail@example.com")
		require.NoError(t, repo.Create(ctx, u1))

		u2 := newTestUser("emaildup2", "sharedemail@example.com")
		err := repo.Create(ctx, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
	})
}

// ---------------------------------------------------------------------------
// TestPGUserRepository_Integration_ConcurrentInsert
// ---------------------------------------------------------------------------

// TestPGUserRepository_Integration_ConcurrentInsert races N goroutines each
// trying to INSERT a user with the same username. Only one must succeed; all
// others must return ErrAuthUserDuplicate. This is the "concurrent INSERT race"
// required by the task spec.
func TestPGUserRepository_Integration_ConcurrentInsert(t *testing.T) {
	repo, cleanup := setupUserRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	const N = 10
	username := "raceuser-" + uuid.NewString()[:8]
	email := func(i int) string {
		return "raceuser" + string(rune('a'+i)) + "@example.com"
	}

	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)

	for i := range N {
		go func(idx int) {
			defer wg.Done()
			u := newTestUser(username, email(idx))
			errs[idx] = repo.Create(ctx, u)
		}(i)
	}
	wg.Wait()

	// Exactly one insert must succeed; the rest must be ErrAuthUserDuplicate.
	successCount := 0
	duplicateCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
			continue
		}
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec, "unexpected error type: %v", err)
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
		duplicateCount++
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent INSERT must succeed")
	assert.Equal(t, N-1, duplicateCount, "all other concurrent INSERTs must be ErrAuthUserDuplicate")
}
