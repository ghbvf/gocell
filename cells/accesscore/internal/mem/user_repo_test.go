package mem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestUserRepo_PreservesPasswordResetRequired(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("testuser", "test@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-test-001"
	user.MarkPasswordResetRequired(time.Now())
	user.CreationSource = domain.UserSourceSetup

	require.NoError(t, repo.Create(ctx, user))

	// GetByID should preserve the flag.
	got, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.True(t, got.PasswordResetRequired, "GetByID must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got.CreationSource, "GetByID must preserve CreationSource")

	// GetByUsername should preserve the flag.
	got2, err := repo.GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.True(t, got2.PasswordResetRequired, "GetByUsername must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got2.CreationSource, "GetByUsername must preserve CreationSource")

	// ApplyPatch should persist changes to the flag.
	prf := false
	now := time.Now()
	patched, err := repo.ApplyPatch(ctx, ports.UserPatch{
		ID:                    "usr-test-001",
		PasswordResetRequired: &prf,
		UpdatedAt:             now,
		CurrentVersion:        got.Version,
	})
	require.NoError(t, err)
	assert.False(t, patched.PasswordResetRequired, "ApplyPatch must persist ClearPasswordResetRequired")
}

func TestUserRepo_ApplyPatch_VersionCAS(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("casusr", "cas@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-cas-001"
	require.NoError(t, repo.Create(ctx, user))

	got, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Version)

	// First patch succeeds and bumps version to 2.
	newEmail := "cas-updated@example.com"
	patched, err := repo.ApplyPatch(ctx, ports.UserPatch{
		ID:             user.ID,
		Email:          &newEmail,
		UpdatedAt:      time.Now(),
		CurrentVersion: got.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), patched.Version)
	assert.Equal(t, newEmail, patched.Email)

	// Second patch with stale version (1) must fail with ErrAuthConcurrentUpdate.
	anotherEmail := "stale@example.com"
	_, err = repo.ApplyPatch(ctx, ports.UserPatch{
		ID:             user.ID,
		Email:          &anotherEmail,
		UpdatedAt:      time.Now(),
		CurrentVersion: 1, // stale
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthConcurrentUpdate, ec.Code)
}

func TestUserRepo_ApplyPatch_EmailDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	u1, err := domain.NewUser("user1", "shared@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u1.ID = "usr-email-1"
	require.NoError(t, repo.Create(ctx, u1))

	u2, err := domain.NewUser("user2", "other@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u2.ID = "usr-email-2"
	require.NoError(t, repo.Create(ctx, u2))

	got, err := repo.GetByID(ctx, u2.ID)
	require.NoError(t, err)

	// Try to patch u2's email to u1's already-taken email.
	taken := "shared@example.com"
	_, err = repo.ApplyPatch(ctx, ports.UserPatch{
		ID:             u2.ID,
		Email:          &taken,
		UpdatedAt:      time.Now(),
		CurrentVersion: got.Version,
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthEmailDuplicate, ec.Code)
}

func TestUserRepo_Create_EmailDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	u1, err := domain.NewUser("user-ed1", "dup@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u1.ID = "usr-ed-1"
	require.NoError(t, repo.Create(ctx, u1))

	u2, err := domain.NewUser("user-ed2", "dup@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u2.ID = "usr-ed-2"
	err = repo.Create(ctx, u2)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthEmailDuplicate, ec.Code)
}

func TestUserRepo_GetByUsernameForUpdate_Equivalence(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("foru-user", "foru@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-foru-001"
	require.NoError(t, repo.Create(ctx, user))

	byName, err := repo.GetByUsername(ctx, "foru-user")
	require.NoError(t, err)

	forUpdate, err := repo.GetByUsernameForUpdate(ctx, "foru-user")
	require.NoError(t, err)

	// Both calls must return identical data.
	assert.Equal(t, byName.ID, forUpdate.ID)
	assert.Equal(t, byName.Version, forUpdate.Version)
	assert.Equal(t, byName.Email, forUpdate.Email)
}

func TestUserRepo_GetByIDForUpdate_EquivalenceAndClone(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("foru-id", "foru-id@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-foru-id-001"
	require.NoError(t, repo.Create(ctx, user))

	byID, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)

	forUpdate, err := repo.GetByIDForUpdate(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, byID.ID, forUpdate.ID)
	assert.Equal(t, byID.Version, forUpdate.Version)
	assert.Equal(t, byID.Email, forUpdate.Email)

	forUpdate.Username = "mutated-copy"
	again, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "foru-id", again.Username)
}

func TestUserRepo_ForUpdateLookups_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	_, err := repo.GetByIDForUpdate(ctx, "usr-missing")
	require.Error(t, err)
	var idErr *errcode.Error
	require.ErrorAs(t, err, &idErr)
	assert.Equal(t, errcode.ErrAuthUserNotFound, idErr.Code)

	_, err = repo.GetByUsernameForUpdate(ctx, "missing")
	require.Error(t, err)
	var usernameErr *errcode.Error
	require.ErrorAs(t, err, &usernameErr)
	assert.Equal(t, errcode.ErrAuthUserNotFound, usernameErr.Code)
}

func TestUserRepo_ApplyPatch_AllMutableFields(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("patch-all", "patch-all@example.com", "$2a$12$old", time.Now())
	require.NoError(t, err)
	user.ID = "usr-patch-all-001"
	require.NoError(t, repo.Create(ctx, user))

	got, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)

	username := "patch-all-updated"
	email := "patch-all-updated@example.com"
	passwordHash := "$2a$12$new"
	passwordResetRequired := true
	status := domain.StatusLocked
	patched, err := repo.ApplyPatch(ctx, ports.UserPatch{
		ID:                    user.ID,
		Username:              &username,
		Email:                 &email,
		PasswordHash:          &passwordHash,
		PasswordResetRequired: &passwordResetRequired,
		Status:                &status,
		UpdatedAt:             time.Now(),
		CurrentVersion:        got.Version,
	})
	require.NoError(t, err)

	assert.Equal(t, username, patched.Username)
	assert.Equal(t, email, patched.Email)
	assert.Equal(t, passwordHash, patched.PasswordHash)
	assert.True(t, patched.PasswordResetRequired)
	assert.Equal(t, domain.StatusLocked, patched.Status)
	assert.Equal(t, got.Version+1, patched.Version)

	byNewName, err := repo.GetByUsername(ctx, username)
	require.NoError(t, err)
	assert.Equal(t, user.ID, byNewName.ID)

	_, err = repo.GetByUsername(ctx, "patch-all")
	require.Error(t, err)
}

func TestUserRepo_ApplyPatch_UsernameDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	u1, err := domain.NewUser("taken", "taken@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u1.ID = "usr-taken"
	require.NoError(t, repo.Create(ctx, u1))

	u2, err := domain.NewUser("rename-me", "rename-me@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u2.ID = "usr-rename"
	require.NoError(t, repo.Create(ctx, u2))

	got, err := repo.GetByID(ctx, u2.ID)
	require.NoError(t, err)
	username := "taken"
	_, err = repo.ApplyPatch(ctx, ports.UserPatch{
		ID:             u2.ID,
		Username:       &username,
		UpdatedAt:      time.Now(),
		CurrentVersion: got.Version,
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
}

func TestUserRepo_Delete_RemovesIndexes(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("delete-me", "delete-me@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-delete"
	require.NoError(t, repo.Create(ctx, user))

	require.NoError(t, repo.Delete(ctx, user.ID))
	_, err = repo.GetByID(ctx, user.ID)
	require.Error(t, err)
	_, err = repo.GetByUsername(ctx, user.Username)
	require.Error(t, err)
}

func TestUserRepo_ApplyPatch_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	_, err := repo.ApplyPatch(ctx, ports.UserPatch{
		ID:             "usr-ghost",
		UpdatedAt:      time.Now(),
		CurrentVersion: 1,
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
}
