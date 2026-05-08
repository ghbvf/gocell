package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewPGUserRepository_RequiresPool(t *testing.T) {
	_, err := NewPGUserRepository(nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "pool must not be nil")
}

func TestNewPGUserRepository_HappyPath(t *testing.T) {
	repo, err := NewPGUserRepository(dummyPool())
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPGUserRepository_ForUpdateRequiresAmbientTx(t *testing.T) {
	repo, err := NewPGUserRepository(dummyPool())
	require.NoError(t, err)

	_, err = repo.GetByIDForUpdate(context.Background(), "usr-1")
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, err.Error(), "FOR UPDATE query requires ambient transaction")
}
