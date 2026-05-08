//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// newTestSessionForUser builds a minimal domain.Session for the given userID.
func newTestSessionForUser(userID string) *domain.Session {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Session{
		ID:          uuid.NewString(),
		UserID:      userID,
		AccessToken: "tok-" + uuid.NewString(),
		ExpiresAt:   now.Add(time.Hour),
		CreatedAt:   now,
		Version:     1,
	}
}

// TestRevokeByIDAndOwner_Soft verifies soft-revoke semantics:
// the row must remain in the sessions table with revoked_at set,
// and GetByID must return ErrSessionNotFound for the revoked session.
func TestRevokeByIDAndOwner_Soft(t *testing.T) {
	pool := setupPGPool(t)
	ctx := context.Background()

	sessionRepo, err := NewPGSessionRepository(pool.DB())
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

	// Revoke s1 by ID+owner.
	require.NoError(t, sessionRepo.RevokeByIDAndOwner(ctx, s1.ID, userID))

	// Assert: s1 row still exists in the DB with revoked_at set.
	var revokedAt *time.Time
	err = rawPool.QueryRow(ctx,
		"SELECT revoked_at FROM sessions WHERE id = $1", s1.ID,
	).Scan(&revokedAt)
	require.NoError(t, err, "revoked session row must still exist in sessions table")
	assert.NotNil(t, revokedAt, "revoked_at must be set for a soft-revoked session")

	// Assert: GetByID returns ErrSessionNotFound for the revoked session.
	_, err = sessionRepo.GetByID(ctx, s1.ID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSessionNotFound, ec.Code,
		"GetByID must return ErrSessionNotFound for a soft-revoked session")

	// Assert: s2 is still accessible.
	got, err := sessionRepo.GetByID(ctx, s2.ID)
	require.NoError(t, err)
	assert.Equal(t, s2.ID, got.ID, "non-revoked session must remain accessible")
}

// TestRevokeByUserID_Soft verifies that RevokeByUserID soft-revokes all
// sessions for a user: rows remain with revoked_at set.
func TestRevokeByUserID_Soft(t *testing.T) {
	pool := setupPGPool(t)
	ctx := context.Background()

	sessionRepo, err := NewPGSessionRepository(pool.DB())
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

	// Revoke all sessions for the user.
	require.NoError(t, sessionRepo.RevokeByUserID(ctx, userID))

	// Assert: both rows still exist with revoked_at set.
	for _, sid := range []string{s1.ID, s2.ID} {
		var revokedAt *time.Time
		err := rawPool.QueryRow(ctx,
			"SELECT revoked_at FROM sessions WHERE id = $1", sid,
		).Scan(&revokedAt)
		require.NoError(t, err, "session row must still exist after RevokeByUserID: id=%s", sid)
		assert.NotNil(t, revokedAt,
			"revoked_at must be non-null for all sessions after RevokeByUserID: id=%s", sid)
	}

	// Assert: both sessions invisible via GetByID.
	for _, sid := range []string{s1.ID, s2.ID} {
		_, err := sessionRepo.GetByID(ctx, sid)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code,
			"GetByID must return ErrSessionNotFound after RevokeByUserID: id=%s", sid)
	}
}
