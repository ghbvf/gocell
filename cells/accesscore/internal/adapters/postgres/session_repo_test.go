package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewPGSessionRepository_RequiresPool(t *testing.T) {
	_, err := NewPGSessionRepository(nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "pool must not be nil")
}

func TestNewPGSessionRepository_HappyPath(t *testing.T) {
	repo, err := NewPGSessionRepository(dummyPool())
	require.NoError(t, err)
	assert.NotNil(t, repo)
}
