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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Integration setup
// ---------------------------------------------------------------------------

// setupUserRepoPG returns a PGUserRepository backed by an isolated PG schema.
// It delegates container start + migration to setupPGPool (shared base container,
// one per test binary run — B1 fix).
func setupUserRepoPG(t *testing.T) *PGUserRepository {
	t.Helper()
	pool := setupPGPool(t)
	repo, err := NewPGUserRepository(pool.DB())
	require.NoError(t, err)
	return repo
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
	repo := setupUserRepoPG(t)
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

	t.Run("ApplyPatch", func(t *testing.T) {
		u := newTestUser("carol", "carol@example.com")
		require.NoError(t, repo.Create(ctx, u))

		newEmail := "carol-updated@example.com"
		mustReset := true
		got, err := repo.ApplyPatch(ctx, ports.UserPatch{
			ID:                    u.ID,
			Email:                 &newEmail,
			PasswordResetRequired: &mustReset,
			UpdatedAt:             time.Now().UTC().Truncate(time.Microsecond),
			CurrentVersion:        u.Version,
		})
		require.NoError(t, err)
		assert.Equal(t, "carol-updated@example.com", got.Email)
		assert.True(t, got.PasswordResetRequired)
		assert.Equal(t, u.Version+1, got.Version, "version must increment")
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

	t.Run("ApplyPatch_NotFound", func(t *testing.T) {
		newEmail := "ghost@example.com"
		_, err := repo.ApplyPatch(ctx, ports.UserPatch{
			ID:             uuid.NewString(), // not in DB
			Email:          &newEmail,
			UpdatedAt:      time.Now().UTC().Truncate(time.Microsecond),
			CurrentVersion: 1,
		})
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
// others must return ErrAuthUserDuplicate.
//
// B5 fix: results are collected via a channel so goroutine writes never share
// a backing array slot, eliminating the theoretical race detector hit on the
// old slice-indexed approach.
func TestPGUserRepository_Integration_ConcurrentInsert(t *testing.T) {
	repo := setupUserRepoPG(t)
	ctx := context.Background()

	const N = 10
	username := "raceuser-" + uuid.NewString()[:8]
	email := func(i int) string {
		return "raceuser" + string(rune('a'+i)) + "@example.com"
	}

	type result struct {
		err error
	}
	resultsCh := make(chan result, N)

	var wg sync.WaitGroup
	wg.Add(N)

	for i := range N {
		go func(idx int) {
			defer wg.Done()
			u := newTestUser(username, email(idx))
			resultsCh <- result{err: repo.Create(ctx, u)}
		}(i)
	}
	wg.Wait()
	close(resultsCh)

	// Exactly one insert must succeed; the rest must be ErrAuthUserDuplicate.
	successCount := 0
	duplicateCount := 0
	for r := range resultsCh {
		if r.err == nil {
			successCount++
			continue
		}
		var ec *errcode.Error
		require.ErrorAs(t, r.err, &ec, "unexpected error type: %v", r.err)
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
		duplicateCount++
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent INSERT must succeed")
	assert.Equal(t, N-1, duplicateCount, "all other concurrent INSERTs must be ErrAuthUserDuplicate")
}
