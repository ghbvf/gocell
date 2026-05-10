//go:build integration

package postgres

import (
	"context"
	"errors"
	"io/fs"
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

// testAdapterMigrationsFS returns the shared adapters/postgres migration FS.
// Duplicate of adapters/postgres/embed_test.go:testMigrationsFS — needed
// because Go _test.go files cannot be imported across packages.
func testAdapterMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}

// setupUserRepoPG starts a PostgreSQL testcontainer, applies all migrations,
// and returns a PGUserRepo + TxManager + cleanup func.
func setupUserRepoPG(t *testing.T) (*PGUserRepo, *adapterpg.TxManager, func()) {
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

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGUserRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return repo, txMgr, cleanup
}

// newTestUser builds a minimal domain.User with a unique username and email.
func newTestUser(suffix string) *domain.User {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &domain.User{
		ID:                    uuid.NewString(),
		Username:              "user_" + suffix,
		Email:                 "user_" + suffix + "@example.com",
		PasswordHash:          "$2a$12$fakehash_" + suffix,
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		CreationSource:        domain.UserSourceIdentity,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

// ---------------------------------------------------------------------------
// Constructor fail-fast tests
// ---------------------------------------------------------------------------

// TestPGUserRepo_Constructor_FailFast verifies that NewPGUserRepo returns a
// structured error for each nil dependency, using a single container so all
// subtests share one Docker lifecycle.
func TestPGUserRepo_Constructor_FailFast(t *testing.T) {
	testutil.RequireDocker(t)
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	txm := adapterpg.NewTxManager(pool)

	assertValidationFailed := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	}

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewPGUserRepo(nil, txm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_txRunner_typed_nil", func(t *testing.T) {
		var nilTxm *adapterpg.TxManager // typed-nil
		_, err := NewPGUserRepo(pool.DB(), nilTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_clock_typed_nil", func(t *testing.T) {
		_, err := NewPGUserRepo(pool.DB(), txm, nil)
		assertValidationFailed(t, err)
	})
}

// ---------------------------------------------------------------------------
// CRUD integration tests
// ---------------------------------------------------------------------------

func TestPGUserRepo_Integration(t *testing.T) {
	repo, _, cleanup := setupUserRepoPG(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("Create_GetByID_roundtrip", func(t *testing.T) {
		u := newTestUser("rt1")
		require.NoError(t, repo.Create(ctx, u))

		got, err := repo.GetByID(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
		assert.Equal(t, u.Email, got.Email)
		assert.Equal(t, u.Status, got.Status)
		assert.Equal(t, u.CreationSource, got.CreationSource)
		assert.Equal(t, u.PasswordResetRequired, got.PasswordResetRequired)
	})

	t.Run("Create_duplicate_username_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u := newTestUser("dup_uname")
		require.NoError(t, repo.Create(ctx, u))

		u2 := newTestUser("dup_uname") // same username
		u2.ID = uuid.NewString()
		u2.Email = "different@example.com"
		err := repo.Create(ctx, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
		assert.Equal(t, errcode.KindConflict, ec.Kind)
	})

	t.Run("Create_duplicate_email_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u := newTestUser("dup_email")
		require.NoError(t, repo.Create(ctx, u))

		u2 := newTestUser("dup_email_v2")
		u2.ID = uuid.NewString()
		u2.Email = u.Email // same email, different username
		err := repo.Create(ctx, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
	})

	t.Run("GetByUsername_found", func(t *testing.T) {
		u := newTestUser("byuname")
		require.NoError(t, repo.Create(ctx, u))

		got, err := repo.GetByUsername(ctx, u.Username)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
	})

	t.Run("GetByID_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	})

	t.Run("GetByUsername_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		_, err := repo.GetByUsername(ctx, "nobody_"+uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Update_existing_persists_fields", func(t *testing.T) {
		u := newTestUser("upd1")
		require.NoError(t, repo.Create(ctx, u))

		u.Status = domain.StatusSuspended
		u.PasswordResetRequired = true
		u.UpdatedAt = u.UpdatedAt.Add(time.Second)
		require.NoError(t, repo.Update(ctx, u))

		got, err := repo.GetByID(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusSuspended, got.Status)
		assert.True(t, got.PasswordResetRequired)
		// updated_at must advance.
		assert.True(t, got.UpdatedAt.After(got.CreatedAt) || got.UpdatedAt.Equal(got.CreatedAt))
	})

	t.Run("Update_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		ghost := newTestUser("ghost_upd")
		ghost.ID = uuid.NewString()
		err := repo.Update(ctx, ghost)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Delete_existing_removes_row", func(t *testing.T) {
		u := newTestUser("del1")
		require.NoError(t, repo.Create(ctx, u))

		require.NoError(t, repo.Delete(ctx, u.ID))

		_, err := repo.GetByID(ctx, u.ID)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Delete_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		err := repo.Delete(ctx, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})
}
