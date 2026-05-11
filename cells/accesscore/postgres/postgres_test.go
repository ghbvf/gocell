package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestNewDeps_NilPool(t *testing.T) {
	tx := &stubTxRunnerForDeps{}
	clk := clock.Real()

	deps, err := NewDeps(nil, tx, clk)

	require.Error(t, err)
	assert.Equal(t, Deps{}, deps)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "pool must not be nil")
}

func TestNewDeps_WrongPoolType(t *testing.T) {
	tx := &stubTxRunnerForDeps{}
	clk := clock.Real()

	deps, err := NewDeps(struct{}{}, tx, clk)

	require.Error(t, err)
	assert.Equal(t, Deps{}, deps)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "pool must be *pgxpool.Pool")
}

func TestNewDeps_NilTxRunner(t *testing.T) {
	pool := &pgxpool.Pool{}
	var nilTx persistence.TxRunner
	clk := clock.Real()

	_, err := NewDeps(pool, nilTx, clk)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "txRunner must not be nil")
}

func TestNewDeps_NilClock(t *testing.T) {
	pool := &pgxpool.Pool{}
	tx := &stubTxRunnerForDeps{}
	var nilClk clock.Clock

	_, err := NewDeps(pool, tx, nilClk)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "clock must not be nil")
}

type stubTxRunnerForDeps struct{}

func (s *stubTxRunnerForDeps) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
