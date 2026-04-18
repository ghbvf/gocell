package mem

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
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

	require.NoError(t, repo.Create(ctx, user))

	// GetByID should preserve the flag.
	got, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.True(t, got.PasswordResetRequired, "GetByID must preserve PasswordResetRequired")

	// GetByUsername should preserve the flag.
	got2, err := repo.GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.True(t, got2.PasswordResetRequired, "GetByUsername must preserve PasswordResetRequired")

	// Update should preserve changes to the flag.
	got.ClearPasswordResetRequired()
	require.NoError(t, repo.Update(ctx, got))

	got3, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.False(t, got3.PasswordResetRequired, "Update must persist ClearPasswordResetRequired")
}
