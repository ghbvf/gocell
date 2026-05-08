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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// newTestSessionForUser builds a minimal domain.Session for the given userID.
func newTestSessionForUser(userID string) *domain.Session {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
		Version:   1,
	}
}

// TestRevokeByIDAndOwner_Soft verifies soft-revoke semantics:
// the row must remain in the sessions table with revoked_at set,
// and GetByID must return ErrSessionNotFound for the revoked session.
func TestRevokeByIDAndOwner_Soft(t *testing.T) {
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

// TestRevokeThenStaleVersionUpdate_DoesNotResurrect (P1#1c) — soft revoke must
// not be reversible via a stale-version Update. Without protection:
//  1. Caller B fetches session → version=1, revoked_at=nil
//  2. Caller A calls RevokeByIDAndOwner → revoked_at=now (version unchanged in
//     naive impl, OR version bumped if revoke increments)
//  3. Caller B sends Update with stale snapshot → SET revoked_at=$5 writes
//     back nil → session resurrected
//
// Two defenses required:
//   - revoke MUST advance version so stale-version Update fails CAS
//   - Update MUST refuse to touch already-revoked rows (defense-in-depth)
func TestRevokeThenStaleVersionUpdate_DoesNotResurrect(t *testing.T) {
	sessionRepo, userRepo, cleanup := setupSessionRepoPG(t)
	defer cleanup()
	ctx := context.Background()
	rawPool := sessionRepo.pool

	userID := seedUser(t, ctx, userRepo)

	// Caller B's snapshot: version=1, revoked_at=nil.
	s := newTestSessionForUser(userID)
	require.NoError(t, sessionRepo.Create(ctx, s))
	stale := *s

	// Caller A revokes the session.
	require.NoError(t, sessionRepo.RevokeByIDAndOwner(ctx, s.ID, userID))

	// Caller B attempts to Update with the stale snapshot. The stale
	// snapshot still has version=1 and revoked_at=nil — naive Update would
	// SET revoked_at=nil, version=2 and resurrect the session.
	stale.ExpiresAt = stale.ExpiresAt.Add(time.Hour)
	updErr := sessionRepo.Update(ctx, &stale)
	require.Error(t, updErr,
		"P1#1c: stale-version Update after revoke MUST fail to prevent resurrection")
	var ec *errcode.Error
	require.ErrorAs(t, updErr, &ec)
	assert.Equal(t, errcode.ErrSessionConflict, ec.Code,
		"stale-version Update after revoke must surface as ErrSessionConflict")

	// Direct DB check: the session row is still revoked, expires_at unchanged.
	var revokedAt *time.Time
	var dbExpiresAt time.Time
	err := rawPool.QueryRow(ctx,
		"SELECT revoked_at, expires_at FROM sessions WHERE id = $1", s.ID,
	).Scan(&revokedAt, &dbExpiresAt)
	require.NoError(t, err)
	assert.NotNil(t, revokedAt, "revoked_at must remain non-null after stale-Update attempt")
	assert.False(t, stale.ExpiresAt.Equal(dbExpiresAt),
		"stale Update must not have rewritten expires_at")
	assert.True(t, s.ExpiresAt.Equal(dbExpiresAt), "expires_at must be the original")

	// GetByID still filters revoked sessions.
	_, getErr := sessionRepo.GetByID(ctx, s.ID)
	require.Error(t, getErr)
	require.ErrorAs(t, getErr, &ec)
	assert.Equal(t, errcode.ErrSessionNotFound, ec.Code,
		"revoked session must remain not-found, not resurrected")
}
