//go:build integration

// PR-V1-PG-ACCESSCORE-REPO B2.A Dev B — PGSessionRepository integration tests.
//
// Exercises the real PostgreSQL backend: CRUD semantics, optimistic-lock
// version CAS, UNIQUE 23505 collision, and owner-scoped deletion.
package postgres

import (
	"context"
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

// setupSessionRepoPG spins up a PostgreSQL container, applies all migrations
// (including 018 for the sessions table), and returns a PGSessionRepository.
func setupSessionRepoPG(t *testing.T) (*PGSessionRepository, func()) {
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

	repo, err := NewPGSessionRepository(pool.DB(), clock.Real())
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

// newTestSession builds a minimal valid domain.Session for test insertion.
func newTestSession(userID, accessToken string) *domain.Session {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Session{
		ID:          uuid.NewString(),
		UserID:      userID,
		AccessToken: accessToken,
		ExpiresAt:   now.Add(time.Hour),
		CreatedAt:   now,
		Version:     1,
	}
}

// ---------------------------------------------------------------------------
// TestPGSessionRepository_Integration
// ---------------------------------------------------------------------------

func TestPGSessionRepository_Integration(t *testing.T) {
	repo, cleanup := setupSessionRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Create_GetByID_HappyPath", func(t *testing.T) {
		s := newTestSession("user-a", "tok-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))

		got, err := repo.GetByID(ctx, s.ID)
		require.NoError(t, err)
		assert.Equal(t, s.ID, got.ID)
		assert.Equal(t, s.UserID, got.UserID)
		assert.Equal(t, s.AccessToken, got.AccessToken)
		assert.Equal(t, s.Version, got.Version)
		assert.True(t, s.ExpiresAt.Equal(got.ExpiresAt))
		assert.Nil(t, got.RevokedAt)
	})

	t.Run("Create_DuplicateAccessToken_ReturnsConflict", func(t *testing.T) {
		token := "dup-tok-" + uuid.NewString()
		s1 := newTestSession("user-dup1", token)
		require.NoError(t, repo.Create(ctx, s1))

		s2 := newTestSession("user-dup2", token)
		err := repo.Create(ctx, s2)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionConflict, ec.Code)
	})

	t.Run("GetByID_NotFound_ReturnsSessionNotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})

	t.Run("Update_CorrectVersion_IncrementsToNext", func(t *testing.T) {
		s := newTestSession("user-upd", "tok-upd-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))
		assert.Equal(t, int64(1), s.Version)

		s.AccessToken = "tok-upd-new-" + uuid.NewString()
		require.NoError(t, repo.Update(ctx, s))
		// Version should have been incremented in-place.
		assert.Equal(t, int64(2), s.Version)

		got, err := repo.GetByID(ctx, s.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), got.Version)
		assert.Equal(t, s.AccessToken, got.AccessToken)
	})

	t.Run("Update_StaleVersion_ReturnsSessionConflict", func(t *testing.T) {
		s := newTestSession("user-stale", "tok-stale-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))

		// First update succeeds, advances version to 2.
		first := *s
		first.AccessToken = "tok-stale-v2-" + uuid.NewString()
		require.NoError(t, repo.Update(ctx, &first))
		assert.Equal(t, int64(2), first.Version)

		// Now try to update with the original version=1 (stale).
		s.AccessToken = "tok-stale-conflict-" + uuid.NewString()
		err := repo.Update(ctx, s)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionConflict, ec.Code)
	})

	t.Run("Update_NotFound_ReturnsSessionNotFound", func(t *testing.T) {
		ghost := newTestSession("user-ghost", "tok-ghost-"+uuid.NewString())
		ghost.ID = uuid.NewString() // not in DB

		err := repo.Update(ctx, ghost)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})

	t.Run("RevokeByIDAndOwner_HappyPath", func(t *testing.T) {
		s := newTestSession("user-rev-owner", "tok-rev-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))

		require.NoError(t, repo.RevokeByIDAndOwner(ctx, s.ID, s.UserID))

		// Row should be deleted.
		_, err := repo.GetByID(ctx, s.ID)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})

	t.Run("RevokeByIDAndOwner_WrongOwner_ReturnsSessionNotFound", func(t *testing.T) {
		s := newTestSession("user-real-owner", "tok-wrong-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))

		err := repo.RevokeByIDAndOwner(ctx, s.ID, "user-wrong-owner")
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})

	t.Run("RevokeByUserID_DeletesAll_ReturnsNilError", func(t *testing.T) {
		userID := "user-revall-" + uuid.NewString()
		sessions := make([]*domain.Session, 3)
		for i := range 3 {
			s := newTestSession(userID, "tok-revall-"+uuid.NewString()+"-"+string(rune('a'+i)))
			require.NoError(t, repo.Create(ctx, s))
			sessions[i] = s
		}

		require.NoError(t, repo.RevokeByUserID(ctx, userID))

		// Strict assertion: every revoked session must be invisible via GetByID.
		for _, s := range sessions {
			_, err := repo.GetByID(ctx, s.ID)
			require.Error(t, err, "session %s must not be found after RevokeByUserID", s.ID)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrSessionNotFound, ec.Code,
				"session %s must return ErrSessionNotFound after RevokeByUserID", s.ID)
		}
	})

	t.Run("Delete_HappyPath", func(t *testing.T) {
		s := newTestSession("user-del", "tok-del-"+uuid.NewString())
		require.NoError(t, repo.Create(ctx, s))

		require.NoError(t, repo.Delete(ctx, s.ID))

		_, err := repo.GetByID(ctx, s.ID)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})

	t.Run("Delete_NotFound_ReturnsSessionNotFound", func(t *testing.T) {
		err := repo.Delete(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrSessionNotFound, ec.Code)
	})
}
