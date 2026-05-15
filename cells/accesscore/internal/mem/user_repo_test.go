package mem

import (
	"context"
	"sync"
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
	user.SetPasswordResetRequired(true, time.Now())
	user.CreationSource = domain.UserSourceSetup

	require.NoError(t, repo.Create(ctx, user))

	// GetByID should preserve the flag.
	got, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.True(t, got.PasswordResetRequired(), "GetByID must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got.CreationSource, "GetByID must preserve CreationSource")

	// GetByUsername should preserve the flag.
	got2, err := repo.GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.True(t, got2.PasswordResetRequired(), "GetByUsername must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got2.CreationSource, "GetByUsername must preserve CreationSource")

	// Update should persist changes to the flag.
	got.SetPasswordResetRequired(false, time.Now())
	require.NoError(t, repo.Update(ctx, got))

	got3, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.False(t, got3.PasswordResetRequired(), "Update must persist SetPasswordResetRequired(false)")
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
	assert.False(t, got.PasswordResetRequired())
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
	user.SetPasswordResetRequired(true, time.Now())
	require.NoError(t, repo.Create(ctx, user))

	// UpdatePassword with resetRequired=false must clear the flag.
	_, err = repo.UpdatePassword(ctx, "usr-carol", "$2a$12$newhash", false, 0)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, "usr-carol")
	require.NoError(t, err)
	assert.False(t, got.PasswordResetRequired(), "UpdatePassword must apply the resetRequired argument")
}

// ---------------------------------------------------------------------------
// BumpAuthzEpoch tests
// ---------------------------------------------------------------------------

func TestUserRepo_BumpAuthzEpoch_IncrementsAndReturns(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("epoch_user", "epoch@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-epoch-001"
	require.NoError(t, repo.Create(ctx, user))

	// First bump: 1 → 2 (initial epoch is 1 per S4d design).
	epoch1, err := repo.BumpAuthzEpoch(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(2), epoch1, "first BumpAuthzEpoch must return 2 (initial=1)")

	// Second bump: 2 → 3.
	epoch2, err := repo.BumpAuthzEpoch(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(3), epoch2, "second BumpAuthzEpoch must return 3")

	// GetByID must reflect the updated epoch.
	got, err := repo.GetByID(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.AuthzEpoch(), "GetByID must return updated AuthzEpoch")
}

func TestUserRepo_BumpAuthzEpoch_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	_, err := repo.BumpAuthzEpoch(ctx, "usr-nonexistent")
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ce.Code)
}

func TestUserRepo_BumpAuthzEpoch_Concurrent(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("concurrent_epoch", "concurrent@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-concurrent-001"
	require.NoError(t, repo.Create(ctx, user))

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = repo.BumpAuthzEpoch(ctx, "usr-concurrent-001")
		}()
	}
	wg.Wait()

	got, err := repo.GetByID(ctx, "usr-concurrent-001")
	require.NoError(t, err)
	assert.Equal(t, int64(goroutines)+1, got.AuthzEpoch(),
		"100 concurrent BumpAuthzEpoch calls starting from epoch=1 must result in epoch == 101")
}
