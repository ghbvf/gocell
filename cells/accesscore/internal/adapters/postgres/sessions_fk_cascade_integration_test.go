//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// TestSessionsFKCascade verifies that deleting a user row cascades to remove
// all associated session rows (ON DELETE CASCADE via fk_sessions_user).
func TestSessionsFKCascade(t *testing.T) {
	pool := setupPGPool(t)
	ctx := context.Background()

	sessionRepo, err := NewPGSessionRepository(pool.DB(), clock.Real())
	require.NoError(t, err)
	userRepo, err := NewPGUserRepository(pool.DB())
	require.NoError(t, err)
	rawPool := pool.DB()

	// Setup: create user + 2 sessions.
	userID := createTestUser(t, ctx, userRepo)
	s1 := newTestSessionForUser(userID)
	s2 := newTestSessionForUser(userID)
	require.NoError(t, sessionRepo.Create(ctx, s1))
	require.NoError(t, sessionRepo.Create(ctx, s2))

	// Verify both sessions exist before deletion.
	var beforeCount int
	err = rawPool.QueryRow(ctx,
		"SELECT COUNT(*) FROM sessions WHERE user_id = $1", userID,
	).Scan(&beforeCount)
	require.NoError(t, err)
	assert.Equal(t, 2, beforeCount, "both sessions must exist before user deletion")

	// Direct SQL delete of the user (bypassing the repo Delete method so we
	// exercise the raw FK CASCADE, not the application-layer cascade).
	_, err = rawPool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	require.NoError(t, err, "direct DELETE of user must succeed")

	// Assert: CASCADE has removed the session rows.
	var afterCount int
	err = rawPool.QueryRow(ctx,
		"SELECT COUNT(*) FROM sessions WHERE user_id = $1", userID,
	).Scan(&afterCount)
	require.NoError(t, err)
	assert.Equal(t, 0, afterCount,
		"all session rows must be CASCADE-deleted when the owning user is deleted (fk_sessions_user)")
}
