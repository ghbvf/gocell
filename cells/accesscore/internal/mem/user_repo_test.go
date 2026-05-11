package mem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestUserRepo_PreservesPasswordResetRequired(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

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

	// Update should persist changes to the flag.
	got.ClearPasswordResetRequired(time.Now())
	require.NoError(t, repo.Update(ctx, got))

	got3, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.False(t, got3.PasswordResetRequired, "Update must persist ClearPasswordResetRequired")
}

func TestUserRepo_UpdatePassword_Match(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("alice", "alice@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-alice"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(ctx, user))

	newPV, err := repo.UpdatePassword(ctx, "usr-alice", "$2a$12$newhash", false, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), newPV, "new password_version must be 1")

	got, err := repo.GetByID(ctx, "usr-alice")
	require.NoError(t, err)
	assert.Equal(t, "$2a$12$newhash", got.PasswordHash)
	assert.Equal(t, int64(1), got.PasswordVersion)
	assert.False(t, got.PasswordResetRequired)
}

func TestUserRepo_UpdatePassword_VersionMismatch(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("bob", "bob@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-bob"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(ctx, user))

	// Provide stale version — should return ErrVersionConflict (KindConflict).
	_, err = repo.UpdatePassword(ctx, "usr-bob", "$2a$12$newhash", false, 99)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindConflict, ce.Kind, "stale version must yield KindConflict")
	assert.Equal(t, errcode.ErrVersionConflict, ce.Code, "stale version must yield ErrVersionConflict")

	// Original hash must be unchanged.
	got, err := repo.GetByID(ctx, "usr-bob")
	require.NoError(t, err)
	assert.Equal(t, "$2a$12$oldhash", got.PasswordHash)
	assert.Equal(t, int64(0), got.PasswordVersion)
}

func TestUserRepo_UpdatePassword_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	_, err := repo.UpdatePassword(ctx, "usr-nonexistent", "$2a$12$newhash", false, 0)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ce.Code)
}

func TestUserRepo_UpdatePassword_ResetRequiredFlag(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("carol", "carol@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-carol"
	user.PasswordVersion = 0
	user.PasswordResetRequired = true
	require.NoError(t, repo.Create(ctx, user))

	// UpdatePassword with resetRequired=false must clear the flag.
	_, err = repo.UpdatePassword(ctx, "usr-carol", "$2a$12$newhash", false, 0)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, "usr-carol")
	require.NoError(t, err)
	assert.False(t, got.PasswordResetRequired, "UpdatePassword must apply the resetRequired argument")
}
