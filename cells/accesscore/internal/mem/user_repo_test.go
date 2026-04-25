package mem

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserRepo_PreservesPasswordResetRequired(t *testing.T) {
	ctx := context.Background()
	repo := NewUserRepository()

	user, err := domain.NewUser("testuser", "test@example.com", "$2a$12$hash")
	require.NoError(t, err)
	user.ID = "usr-test-001"
	user.MarkPasswordResetRequired()
	user.MarkProvisionPending(domain.UserSourceBootstrap)

	require.NoError(t, repo.Create(ctx, user))

	// GetByID should preserve the flag.
	got, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.True(t, got.PasswordResetRequired, "GetByID must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceBootstrap, got.CreationSource, "GetByID must preserve CreationSource")
	assert.Equal(t, domain.ProvisionStatePending, got.ProvisionState, "GetByID must preserve ProvisionState")

	// GetByUsername should preserve the flag.
	got2, err := repo.GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.True(t, got2.PasswordResetRequired, "GetByUsername must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceBootstrap, got2.CreationSource, "GetByUsername must preserve CreationSource")
	assert.Equal(t, domain.ProvisionStatePending, got2.ProvisionState, "GetByUsername must preserve ProvisionState")

	// Update should preserve changes to the flag.
	got.ClearPasswordResetRequired()
	got.MarkProvisionComplete()
	require.NoError(t, repo.Update(ctx, got))

	got3, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.False(t, got3.PasswordResetRequired, "Update must persist ClearPasswordResetRequired")
	assert.Equal(t, domain.ProvisionStateComplete, got3.ProvisionState, "Update must persist provision completion")
}
