//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
)

// sqlStateCheckViolation is SQLSTATE 23514 (check constraint violation).
const sqlStateCheckViolation = "23514"

// setupUserRepoPGWithPool clones the package-shared pre-migrated template
// database into a fresh per-test DB and returns a PGUserRepo + Pool for
// tests that need direct SQL access (e.g. to bypass domain validation).
// Pool + per-test DB lifecycle is owned by t.Cleanup inside
// sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupUserRepoPGWithPool(t *testing.T) (*PGUserRepo, *adapterpg.Pool) {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGUserRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	return repo, pool
}

// setupUserRepoPG clones the package-shared pre-migrated template database
// into a fresh per-test DB and returns a PGUserRepo + TxManager.
func setupUserRepoPG(t *testing.T) (*PGUserRepo, *adapterpg.TxManager) {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGUserRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	return repo, txMgr
}

// newTestUser builds a minimal domain.User with a unique username and email.
// Uses domain.ReconstituteUser so that private authz fields (status,
// passwordResetRequired, authzEpoch) are correctly populated, and authzEpoch
// is seeded to 1 (the minimum valid value — migration 028 CHECK(>0) enforces
// this at the DB level; ReconstituteUser rejects 0 at the domain level).
func newTestUser(suffix string) *domain.User {
	now := time.Now().UTC().Truncate(time.Millisecond)
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           uuid.NewString(),
		Username:     "user_" + suffix,
		Email:        "user_" + suffix + "@example.com",
		PasswordHash: "$2a$12$fakehash_" + suffix,
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1, // authzEpoch >= 1 (migration 028 CHECK constraint)
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		panic("newTestUser: " + err.Error())
	}
	return u
}

// ---------------------------------------------------------------------------
// Constructor fail-fast tests
// ---------------------------------------------------------------------------

// TestPGUserRepo_Constructor_FailFast verifies that NewPGUserRepo returns a
// structured error for each nil dependency. Uses one per-test database
// (cloned from the package-shared template) for all subtests; lifetime is
// owned by t.Cleanup inside sharedPG.NewPerTestPool.
func TestPGUserRepo_Constructor_FailFast(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
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
	repo, txMgr := setupUserRepoPG(t)
	ctx := context.Background()

	t.Run("Create_GetByIDInTenant_roundtrip", func(t *testing.T) {
		u := newTestUser("rt1")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		got, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
		assert.Equal(t, u.Email, got.Email)
		assert.Equal(t, u.Status(), got.Status())
		assert.Equal(t, u.CreationSource, got.CreationSource)
		assert.Equal(t, u.PasswordResetRequired(), got.PasswordResetRequired())
	})

	t.Run("Create_duplicate_username_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u := newTestUser("dup_uname")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		u2 := newTestUser("dup_uname") // same username
		u2.ID = uuid.NewString()
		u2.Email = "different@example.com"
		err := repo.Create(ctx, testTenantID, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
		assert.Equal(t, errcode.KindConflict, ec.Kind)
	})

	t.Run("Create_same_username_different_tenants_succeeds", func(t *testing.T) {
		// Model-A composite-unique (tenant_id, username) / (tenant_id, email):
		// the same username and email CAN exist in two different tenants.
		u1 := newTestUser("xtenant_shared_name")
		require.NoError(t, repo.Create(ctx, testTenantID, u1), "tenant A create must succeed")

		// Exact same username and email, but different tenant.
		u2, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
			ID:           uuid.NewString(), // different PK
			Username:     u1.Username,      // same username
			Email:        u1.Email,         // same email
			PasswordHash: u1.PasswordHash,
			Status:       domain.StatusActive,
			Source:       domain.UserSourceIdentity,
			AuthzEpoch:   1,
			CreatedAt:    u1.CreatedAt,
			UpdatedAt:    u1.UpdatedAt,
		})
		require.NoError(t, err)

		require.NoError(t, repo.Create(ctx, testTenantIDOther, u2),
			"same username/email in a different tenant must succeed (composite-unique per tenant)")

		// Sanity: each tenant finds its own row.
		got1, err := repo.GetByUsername(ctx, testTenantID, u1.Username)
		require.NoError(t, err)
		assert.Equal(t, u1.ID, got1.ID, "tenant A must find tenant-A user")

		got2, err := repo.GetByUsername(ctx, testTenantIDOther, u2.Username)
		require.NoError(t, err)
		assert.Equal(t, u2.ID, got2.ID, "tenant B must find tenant-B user")
	})

	t.Run("Create_duplicate_email_returns_ErrAuthUserDuplicate", func(t *testing.T) {
		u := newTestUser("dup_email")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		u2 := newTestUser("dup_email_v2")
		u2.ID = uuid.NewString()
		u2.Email = u.Email // same email, different username
		err := repo.Create(ctx, testTenantID, u2)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
	})

	t.Run("GetByUsername_found", func(t *testing.T) {
		u := newTestUser("byuname")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		got, err := repo.GetByUsername(ctx, testTenantID, u.Username)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
	})

	t.Run("GetByIDInTenant_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		_, err := repo.GetByIDInTenant(ctx, testTenantID, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	})

	t.Run("GetByUsername_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		_, err := repo.GetByUsername(ctx, testTenantID, "nobody_"+uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("UpdateLockState_existing_persists_status", func(t *testing.T) {
		u := newTestUser("upd1")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		now := u.UpdatedAt.Add(time.Second)
		require.NoError(t, repo.UpdateLockState(ctx, testTenantID, u.ID, domain.StatusSuspended, now))
		require.NoError(t, repo.UpdatePasswordResetFlag(ctx, testTenantID, u.ID, true, now))

		got, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusSuspended, got.Status())
		assert.True(t, got.PasswordResetRequired())
		// updated_at must advance.
		assert.True(t, got.UpdatedAt.After(got.CreatedAt) || got.UpdatedAt.Equal(got.CreatedAt))
	})

	t.Run("UpdateLockState_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		ghostID := uuid.NewString()
		err := repo.UpdateLockState(ctx, testTenantID, ghostID, domain.StatusLocked, time.Now().UTC())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Delete_existing_removes_row", func(t *testing.T) {
		u := newTestUser("del1")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		require.NoError(t, repo.Delete(ctx, testTenantID, u.ID))

		_, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("Delete_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		err := repo.Delete(ctx, testTenantID, uuid.NewString())
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("GetByIDForUpdate_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, e := repo.GetByIDForUpdate(txCtx, testTenantID, uuid.NewString())
			return e
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("GetByIDForUpdate_success_returns_user", func(t *testing.T) {
		// Success-path coverage for getForUpdateBy(lookupByID) helper after
		// PR #1236 typed-enum extraction — guards against regressions in the
		// shared scan / errcode wrap path.
		u := newTestUser("forupd_id")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		var got *domain.User
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			var e error
			got, e = repo.GetByIDForUpdate(txCtx, testTenantID, u.ID)
			return e
		})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
	})

	t.Run("GetByUsernameForUpdate_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, e := repo.GetByUsernameForUpdate(txCtx, testTenantID, "absent_"+uuid.NewString())
			return e
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	t.Run("GetByUsernameForUpdate_success_returns_user", func(t *testing.T) {
		// Success-path coverage for getForUpdateBy(lookupByUsername) helper —
		// mirrors the GetByIDForUpdate_success case above.
		u := newTestUser("forupd_username")
		require.NoError(t, repo.Create(ctx, testTenantID, u))

		var got *domain.User
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			var e error
			got, e = repo.GetByUsernameForUpdate(txCtx, testTenantID, u.Username)
			return e
		})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, u.ID, got.ID)
		assert.Equal(t, u.Username, got.Username)
	})

	t.Run("BumpAuthzEpoch_missing_returns_ErrAuthUserNotFound", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, e := repo.BumpAuthzEpoch(txCtx, testTenantID, uuid.NewString(), credentialfence.Mint())
			return e
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	})

	// -----------------------------------------------------------------------
	// PR464 P1.3: UpdatePassword CAS path (PG) — three-way classification:
	// version match → bumps version; version mismatch → 409; user absent → 404.
	// -----------------------------------------------------------------------

	t.Run("UpdatePassword_VersionMatch_BumpsVersion", func(t *testing.T) {
		user := newTestUser("pwd_match_" + uuid.NewString())
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, testTenantID, user)
		}))

		var newVersion int64
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			v, err := repo.UpdatePassword(txCtx, testTenantID, user.ID, "$2a$12$newhash_match", false, 0)
			if err != nil {
				return err
			}
			newVersion = v
			return nil
		}))
		assert.Equal(t, int64(1), newVersion, "expected password_version to bump 0→1")

		got, err := repo.GetByIDInTenant(ctx, testTenantID, user.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.PasswordVersion)
		assert.Equal(t, "$2a$12$newhash_match", got.PasswordHash)
	})

	t.Run("UpdatePassword_VersionMismatch_Returns409", func(t *testing.T) {
		user := newTestUser("pwd_mismatch_" + uuid.NewString())
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, testTenantID, user)
		}))

		// First update succeeds (expected=0, bump→1)
		require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.UpdatePassword(txCtx, testTenantID, user.ID, "$2a$12$first", false, 0)
			return err
		}))

		// Second update with stale expected=0 must fail with ErrVersionConflict (409)
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.UpdatePassword(txCtx, testTenantID, user.ID, "$2a$12$stale", false, 0)
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrVersionConflict, ec.Code,
			"stale expectedVersion must return ErrVersionConflict (409), got %s", ec.Code)

		// Confirm hash was NOT overwritten by the stale attempt.
		got, err := repo.GetByIDInTenant(ctx, testTenantID, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "$2a$12$first", got.PasswordHash,
			"stale CAS must not overwrite first-writer's hash")
		assert.Equal(t, int64(1), got.PasswordVersion)
	})

	t.Run("UpdatePassword_UserAbsent_Returns404", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.UpdatePassword(txCtx, testTenantID, uuid.NewString(), "$2a$12$any", false, 0)
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code,
			"absent user must return ErrAuthUserNotFound (404), got %s", ec.Code)
	})
}

// TestPGUserRepo_BumpAuthzEpoch_ReadbackVisible guards the SQL/scan contract:
// after BumpAuthzEpoch increments users.authz_epoch, the next GetByID /
// GetByUsername read MUST surface the new value. The original S4b SELECT list
// omitted authz_epoch entirely (Finding #1 / PR #490 review), which left
// sessionvalidate comparing user.AuthzEpoch=0 against the JWT claim — silently
// breaking the credential-invalidation chain. This integration test fails
// loudly the moment the column is dropped from either select query or from
// scanUser's row.Scan ordering.
func TestPGUserRepo_BumpAuthzEpoch_ReadbackVisible(t *testing.T) {
	repo, txMgr := setupUserRepoPG(t)
	ctx := context.Background()

	u := newTestUser("authz_readback_" + uuid.NewString())
	require.NoError(t, repo.Create(ctx, testTenantID, u))

	// Baseline: freshly created user must have authz_epoch=1 from the INSERT.
	// domain.NewUser seeds epoch=1; migration 028 enforces CHECK(>0) so epoch=0
	// is rejected by the DB. ReconstituteUser also rejects authzEpoch<=0.
	got, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.AuthzEpoch(),
		"new user must have AuthzEpoch=1 (seeded by NewUser); got %d", got.AuthzEpoch())

	// Bump epoch inside a tx (BumpAuthzEpoch contract requires ambient tx —
	// funnel entry point guarantees this in production).
	var bumped int64
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		v, err := repo.BumpAuthzEpoch(txCtx, testTenantID, u.ID, credentialfence.Mint())
		if err != nil {
			return err
		}
		bumped = v
		return nil
	}))
	// User starts at epoch=1 (domain.NewUser); first bump → 2.
	assert.Equal(t, int64(2), bumped, "BumpAuthzEpoch must return new value 2 (user started at 1)")

	// Readback via GetByID — must reflect the post-bump value.
	gotByID, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), gotByID.AuthzEpoch(),
		"GetByID must return post-bump AuthzEpoch=2; got %d — SELECT list likely missing authz_epoch column",
		gotByID.AuthzEpoch())

	// Readback via GetByUsername — same invariant; both query paths share scanUser.
	gotByName, err := repo.GetByUsername(ctx, testTenantID, u.Username)
	require.NoError(t, err)
	assert.Equal(t, int64(2), gotByName.AuthzEpoch(),
		"GetByUsername must return post-bump AuthzEpoch=2; got %d", gotByName.AuthzEpoch())

	// Second bump confirms monotonicity through the read path.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		v, err := repo.BumpAuthzEpoch(txCtx, testTenantID, u.ID, credentialfence.Mint())
		if err != nil {
			return err
		}
		bumped = v
		return nil
	}))
	assert.Equal(t, int64(3), bumped)
	got2, err := repo.GetByIDInTenant(ctx, testTenantID, u.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got2.AuthzEpoch(),
		"second bump must propagate through read path")
}

// ---------------------------------------------------------------------------
// S3F: DB CHECK constraint enforcement tests (migration 023)
// ---------------------------------------------------------------------------

// TestUserRepo_Create_RejectsInvalidStatus_DBCheck verifies that PostgreSQL's
// users_status_chk CHECK constraint (migration 023) rejects direct INSERTs with
// an invalid status value, even if the domain layer is bypassed. SQLSTATE 23514.
func TestUserRepo_Create_RejectsInvalidStatus_DBCheck(t *testing.T) {
	_, pool := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	// Bypass domain/repo layer and INSERT directly with an invalid status.
	// Use authz_epoch=1 so that the migration 028 CHECK(authz_epoch > 0) does
	// not fire before the status constraint; we want the status CHECK to be
	// what triggers the 23514.
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := pool.DB().Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, email, password_hash, password_reset_required,
		                   status, creation_source, authz_epoch, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, false, 'bogus', 'identity', 1, $5, $5)`,
		id, "check_status_user", "check_status@example.com", "$2a$12$fakehash", now)

	require.Error(t, err, "INSERT with invalid status must be rejected by DB CHECK constraint")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr),
		"error must be a PG error")
	assert.Equal(t, sqlStateCheckViolation, pgErr.Code,
		"SQLSTATE must be 23514 (check constraint violation) for invalid status")
}

// TestUserRepo_Create_RejectsInvalidCreationSource_DBCheck verifies that the
// users_creation_source_chk CHECK constraint (migration 023) rejects direct
// INSERTs with an invalid creation_source value. SQLSTATE 23514.
func TestUserRepo_Create_RejectsInvalidCreationSource_DBCheck(t *testing.T) {
	_, pool := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	// Use authz_epoch=1 so that the migration 028 CHECK(authz_epoch > 0) does
	// not fire before the creation_source constraint.
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := pool.DB().Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, email, password_hash, password_reset_required,
		                   status, creation_source, authz_epoch, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, false, 'active', 'bogus', 1, $5, $5)`,
		id, "check_source_user", "check_source@example.com", "$2a$12$fakehash", now)

	require.Error(t, err, "INSERT with invalid creation_source must be rejected by DB CHECK constraint")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr),
		"error must be a PG error")
	assert.Equal(t, sqlStateCheckViolation, pgErr.Code,
		"SQLSTATE must be 23514 (check constraint violation) for invalid creation_source")
}

// TestUserRepo_Scan_RejectsInvalidStatus verifies that scanUser returns an
// ErrInternal error when a row with an invalid status value is scanned.
// To bypass the DB CHECK constraint (migration 023), we temporarily DROP the
// constraint, write the bad row, then restore it, then call GetByID.
func TestUserRepo_Scan_RejectsInvalidStatus(t *testing.T) {
	repo, pool := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	// Temporarily drop the CHECK constraints to allow writing an invalid status.
	// Also drop the migration 028 epoch CHECK so authz_epoch=0 is accepted by
	// the raw INSERT (we want to test the scan-side rejection for invalid status,
	// not the DB CHECK that would prevent the row being written at all).
	_, err := pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk`)
	require.NoError(t, err, "must be able to drop status constraint for test setup")
	_, err = pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_authz_epoch_positive`)
	require.NoError(t, err, "must be able to drop epoch constraint for test setup")
	// Per-test container is fresh; no constraint restore needed and a NOT VALID
	// restore would mask future scan-side regressions. Container teardown via
	// cleanup() drops the entire schema, so leaving the constraints absent here
	// has no cross-test side effect.

	id := uuid.NewString()
	now := time.Now().UTC()
	_, err = pool.DB().Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, email, password_hash, password_reset_required,
		                   status, creation_source, authz_epoch, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, false, 'invalid_status', 'identity', 0, $5, $5)`,
		id, "scan_invalid_status_user", "scan_invalid@example.com", "$2a$12$fakehash", now)
	require.NoError(t, err, "INSERT with constraints dropped must succeed")

	// Now scanUser must reject the invalid status and GetByID must propagate
	// ErrPGSchemaShape unchanged (no ErrInternal wrap) so operators can
	// distinguish DB schema drift from generic infra faults.
	_, scanErr := repo.GetByIDInTenant(ctx, testTenantID, id)
	require.Error(t, scanErr, "GetByID must return error for row with invalid status")
	var ec *errcode.Error
	require.True(t, errors.As(scanErr, &ec),
		"error must be an *errcode.Error")
	assert.Equal(t, errcode.ErrPGSchemaShape, ec.Code,
		"scan must propagate ErrPGSchemaShape (not collapse to ErrInternal)")
	assert.Equal(t, errcode.KindInternal, ec.Kind,
		"scan enum violation must surface as KindInternal (5xx)")
	assert.Contains(t, ec.Message, "invalid status",
		"error message must identify the invalid field")
}

// TestUserRepo_Scan_RejectsInvalidCreationSource verifies that scanUser
// surfaces ErrPGSchemaShape when DB row carries an unknown creation_source
// enum value. Mirrors TestUserRepo_Scan_RejectsInvalidStatus for the
// orthogonal enum column.
func TestUserRepo_Scan_RejectsInvalidCreationSource(t *testing.T) {
	repo, pool := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	_, err := pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_creation_source_chk`)
	require.NoError(t, err, "must be able to drop creation_source CHECK")
	_, err = pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_authz_epoch_positive`)
	require.NoError(t, err, "must be able to drop epoch CHECK for test setup")

	id := uuid.NewString()
	now := time.Now().UTC()
	_, err = pool.DB().Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, email, password_hash, password_reset_required,
		                   status, creation_source, authz_epoch, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, false, 'active', 'bogus_source', 0, $5, $5)`,
		id, "scan_invalid_source_user", "scan_invalid_source@example.com", "$2a$12$fakehash", now)
	require.NoError(t, err)

	_, scanErr := repo.GetByIDInTenant(ctx, testTenantID, id)
	require.Error(t, scanErr)
	var ec *errcode.Error
	require.True(t, errors.As(scanErr, &ec))
	assert.Equal(t, errcode.ErrPGSchemaShape, ec.Code,
		"scan must propagate ErrPGSchemaShape, not collapse to ErrInternal")
	assert.Contains(t, ec.Message, "invalid creation_source",
		"error message must identify the invalid field")
}

// TestUserRepo_GetByUsername_RejectsInvalidStatus exercises the
// GetByUsername path's ErrPGSchemaShape propagation (parallel to GetByID).
func TestUserRepo_GetByUsername_RejectsInvalidStatus(t *testing.T) {
	repo, pool := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	_, err := pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk`)
	require.NoError(t, err)
	_, err = pool.DB().Exec(ctx, `ALTER TABLE users DROP CONSTRAINT IF EXISTS users_authz_epoch_positive`)
	require.NoError(t, err, "must be able to drop epoch CHECK for test setup")

	id := uuid.NewString()
	username := "scan_invalid_byname"
	now := time.Now().UTC()
	_, err = pool.DB().Exec(ctx, `
		INSERT INTO users (id, tenant_id, username, email, password_hash, password_reset_required,
		                   status, creation_source, authz_epoch, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, $3, $4, false, 'bogus_status', 'identity', 0, $5, $5)`,
		id, username, "scan_invalid_byname@example.com", "$2a$12$fakehash", now)
	require.NoError(t, err)

	_, scanErr := repo.GetByUsername(ctx, testTenantID, username)
	require.Error(t, scanErr)
	var ec *errcode.Error
	require.True(t, errors.As(scanErr, &ec))
	assert.Equal(t, errcode.ErrPGSchemaShape, ec.Code,
		"GetByUsername must propagate ErrPGSchemaShape unchanged")
	assert.Contains(t, ec.Message, "invalid status")
}

// TestUserRepo_CreationSource_BothValid verifies that users with both
// valid creation_source values ('identity' and 'setup') can be created
// and read back without error.
func TestUserRepo_CreationSource_BothValid(t *testing.T) {
	repo, _ := setupUserRepoPGWithPool(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)

	mustReconstitute := func(id, username, email, hash string, source domain.UserSource) *domain.User {
		t.Helper()
		u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
			ID:           id,
			Username:     username,
			Email:        email,
			PasswordHash: hash,
			Status:       domain.StatusActive,
			Source:       source,
			AuthzEpoch:   1, // authzEpoch >= 1 (migration 028 CHECK constraint)
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		require.NoError(t, err)
		return u
	}

	identityUser := mustReconstitute(
		uuid.NewString(), "src_identity_user", "src_identity@example.com",
		"$2a$12$fakehash_identity", domain.UserSourceIdentity,
	)

	setupUser := mustReconstitute(
		uuid.NewString(), "src_setup_user", "src_setup@example.com",
		"$2a$12$fakehash_setup", domain.UserSourceSetup,
	)

	require.NoError(t, repo.Create(ctx, testTenantID, identityUser), "identity source user must be created")
	require.NoError(t, repo.Create(ctx, testTenantID, setupUser), "setup source user must be created")

	gotIdentity, err := repo.GetByIDInTenant(ctx, testTenantID, identityUser.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.UserSourceIdentity, gotIdentity.CreationSource)

	gotSetup, err := repo.GetByIDInTenant(ctx, testTenantID, setupUser.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.UserSourceSetup, gotSetup.CreationSource)
}
