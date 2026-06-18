package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// fakeTx is a minimal pgx.Tx implementation that satisfies the interface for
// context-injection tests. resolve returns it (identity); the Exec/Query/QueryRow
// overrides let the dbtxAdapter delegation be exercised without a real DB.
type fakeTx struct {
	pgx.Tx
}

func (f *fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func TestNewSession_ReturnsSession(t *testing.T) {
	assert.NotNil(t, NewSession(nil))
}

func TestNewRegistry_ConstructsWithSession(t *testing.T) {
	r := NewRegistry(nil, clock.Real())
	require.NotNil(t, r)
	require.NotNil(t, r.session)
}

func TestSession_Resolve_ReturnsTxWhenPresent(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, pgx.Tx(tx))
	s := &Session{pool: nil}

	adapter, ok := s.resolve(ctx).(*dbtxAdapter)
	require.True(t, ok, "resolve should return *dbtxAdapter when tx is in ctx")
	assert.Equal(t, pgx.Tx(tx), adapter.tx)
}

func TestSession_Resolve_FallsBackToPoolWhenNoTx(t *testing.T) {
	s := &Session{pool: nil}
	_, ok := s.resolve(context.Background()).(*poolAdapter)
	require.True(t, ok, "resolve should return *poolAdapter when no tx in ctx")
}

func TestSession_ResolveWrite_WithTx_ReturnsAdapter(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, pgx.Tx(tx))
	s := &Session{pool: nil}

	db, err := s.resolveWrite(ctx)
	require.NoError(t, err)
	adapter, ok := db.(*dbtxAdapter)
	require.True(t, ok)
	assert.Equal(t, pgx.Tx(tx), adapter.tx)
}

func TestSession_ResolveWrite_WithoutTx_ReturnsErrAdapterPGNoTx(t *testing.T) {
	s := &Session{pool: nil}
	_, err := s.resolveWrite(context.Background())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestRegistry_ResolveRead_UsesSessionWhenPresent verifies the production
// resolveRead delegates to the session (tx-in-ctx path) rather than the test db.
func TestRegistry_ResolveRead_UsesSessionWhenPresent(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, pgx.Tx(tx))
	r := &Registry{session: &Session{pool: nil}, clk: clock.Real()}

	_, ok := r.resolveRead(ctx).(*dbtxAdapter)
	require.True(t, ok)
}

func TestRegistry_ResolveWrite_RequiresTxInProduction(t *testing.T) {
	r := &Registry{session: &Session{pool: nil}, clk: clock.Real()}
	_, err := r.resolveWrite(context.Background())
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterPGNoTx, ec.Code)
}

// TestDBTXAdapter_ExecDelegatesToTx exercises dbtxAdapter.Exec forwarding (the
// positive tx path: pgconn.CommandTag → int64 RowsAffected) without a real DB.
// Query/QueryRow delegation is integration-covered (they need a live cursor).
func TestDBTXAdapter_ExecDelegatesToTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, pgx.Tx(tx))
	db := (&Session{pool: nil}).resolve(ctx)

	n, err := db.Exec(ctx, "INSERT 1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}
