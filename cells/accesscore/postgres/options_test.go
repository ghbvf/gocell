package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestWithPoolRejectsNilPool(t *testing.T) {
	opts, err := WithPool(nil, clock.Real())
	require.Error(t, err)
	assert.Nil(t, opts)

	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrCellInvalidConfig, coded.Code)
	assert.Contains(t, err.Error(), "non-nil *pgxpool.Pool")
}

func TestWithPoolRejectsNilClock(t *testing.T) {
	opts, err := WithPool(&pgxpool.Pool{}, nil)
	require.Error(t, err)
	assert.Nil(t, opts)

	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrCellInvalidConfig, coded.Code)
	assert.Contains(t, err.Error(), "non-nil clock.Clock")
}

func TestWithPoolBuildsRepositoryOptions(t *testing.T) {
	opts, err := WithPool(&pgxpool.Pool{}, clock.Real())
	require.NoError(t, err)
	assert.Len(t, opts, 3)
}
