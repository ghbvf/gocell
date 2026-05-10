package accesscore

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

// TestNewPGDeps_NilPool verifies that passing nil as pool returns ErrValidationFailed
// with "pool must not be nil" message.
func TestNewPGDeps_NilPool(t *testing.T) {
	tx := &stubTxRunnerForDeps{}
	clk := clock.Real()

	deps, err := NewPGDeps(nil, tx, clk)

	require.Error(t, err)
	assert.Equal(t, PGDeps{}, deps)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "pool must not be nil")
}

// TestNewPGDeps_WrongPoolType verifies that passing a non-*pgxpool.Pool value returns
// ErrValidationFailed with "pool must be *pgxpool.Pool" message.
func TestNewPGDeps_WrongPoolType(t *testing.T) {
	tx := &stubTxRunnerForDeps{}
	clk := clock.Real()

	deps, err := NewPGDeps(struct{}{}, tx, clk)

	require.Error(t, err)
	assert.Equal(t, PGDeps{}, deps)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "pool must be *pgxpool.Pool")
}

// TestNewPGDeps_NilTxRunner verifies that a typed-nil TxRunner is rejected with
// "txRunner must not be nil". A zero-value *pgxpool.Pool satisfies the type
// assertion so the txRunner guard fires next; the pool is never dereferenced
// because construction fails before the body returns the populated PGDeps.
func TestNewPGDeps_NilTxRunner(t *testing.T) {
	pool := &pgxpool.Pool{}
	var nilTx persistence.TxRunner // typed nil interface
	clk := clock.Real()

	_, err := NewPGDeps(pool, nilTx, clk)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "txRunner must not be nil")
}

// TestNewPGDeps_NilClock verifies that a typed-nil clock.Clock is rejected with
// "clock must not be nil". Same pool-zero-value pattern as TestNewPGDeps_NilTxRunner.
func TestNewPGDeps_NilClock(t *testing.T) {
	pool := &pgxpool.Pool{}
	tx := &stubTxRunnerForDeps{}
	var nilClk clock.Clock // typed nil interface

	_, err := NewPGDeps(pool, tx, nilClk)

	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "clock must not be nil")
}

// stubTxRunnerForDeps is a minimal non-nil persistence.TxRunner for unit tests.
type stubTxRunnerForDeps struct{}

func (s *stubTxRunnerForDeps) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
