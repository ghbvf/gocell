package postgres

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTx is a minimal pgx.Tx implementation that satisfies the interface for
// context-injection tests. Only the identity is tested (resolve returns it);
// method calls are not exercised by session unit tests.
type fakeTx struct {
	pgx.Tx // embed to satisfy the interface; unused methods panic implicitly
}

func TestSessionFromCtx_ReturnsTxWhenPresent(t *testing.T) {
	// Arrange: put a fake pgx.Tx into ctx via persistence.TxCtxKey.
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, pgx.Tx(tx))

	// Session with nil pool — should never be reached if tx is resolved.
	s := &Session{pool: nil}
	db := s.resolve(ctx)

	// Assert: the resolved DBTX wraps the tx (dbtxAdapter).
	adapter, ok := db.(*dbtxAdapter)
	require.True(t, ok, "resolve should return *dbtxAdapter when tx is in ctx")
	assert.Equal(t, pgx.Tx(tx), adapter.tx)
}

func TestSessionFromCtx_ReturnsPoolWhenNoTx(t *testing.T) {
	// Arrange: ctx has no transaction.
	ctx := context.Background()

	// We cannot construct a real pgxpool.Pool in unit tests, so we verify
	// the type switch only — resolve returns *poolAdapter when no tx is in ctx.
	s := &Session{pool: nil} // pool is nil; test only checks type switch
	db := s.resolve(ctx)

	_, ok := db.(*poolAdapter)
	require.True(t, ok, "resolve should return *poolAdapter when no tx in ctx")
}

func TestNewSession_ReturnsSession(t *testing.T) {
	s := NewSession(nil) // nil pool is fine for construction
	assert.NotNil(t, s)
}
