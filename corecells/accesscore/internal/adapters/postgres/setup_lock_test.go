package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewPGSetupLock_NilTxRunner verifies that passing a nil TxRunner returns
// ErrValidationFailed at construction time.
func TestNewPGSetupLock_NilTxRunner(t *testing.T) {
	lock, err := NewPGSetupLock(nil)

	require.Error(t, err)
	assert.Nil(t, lock)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "txRunner must not be nil")
}

// TestPGSetupLock_Acquire_NoTxInCtx verifies that Acquire returns ErrInternal
// when the context does not carry a pgx.Tx under persistence.TxCtxKey.
func TestPGSetupLock_Acquire_NoTxInCtx(t *testing.T) {
	// Use a non-nil typed TxRunner stub so NewPGSetupLock succeeds.
	stub := &stubTxRunner{}
	lock, err := NewPGSetupLock(stub)
	require.NoError(t, err)

	ctx := context.Background() // no pgx.Tx injected
	acquireErr := lock.Acquire(ctx)

	require.Error(t, acquireErr)
	var ecErr *errcode.Error
	require.ErrorAs(t, acquireErr, &ecErr)
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Contains(t, ecErr.Message, "no pgx.Tx in ctx")
}

// TestPGSetupLock_Acquire_TypedNilTx verifies that Acquire returns ErrInternal
// when the context carries a typed-nil pgx.Tx (interface set but value is nil).
func TestPGSetupLock_Acquire_TypedNilTx(t *testing.T) {
	stub := &stubTxRunner{}
	lock, err := NewPGSetupLock(stub)
	require.NoError(t, err)

	// Inject a typed-nil pgx.Tx: the type assertion in Acquire succeeds (!ok=false
	// is NOT triggered), but the nil-check `tx == nil` catches it.
	var nilTx pgx.Tx // typed nil interface
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, nilTx)
	acquireErr := lock.Acquire(ctx)

	require.Error(t, acquireErr)
	var ecErr *errcode.Error
	require.ErrorAs(t, acquireErr, &ecErr)
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Contains(t, ecErr.Message, "no pgx.Tx in ctx")
}

// stubTxRunner is a minimal non-nil persistence.TxRunner implementation
// used only to pass NewPGSetupLock's nil guard in unit tests.
type stubTxRunner struct{}

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
