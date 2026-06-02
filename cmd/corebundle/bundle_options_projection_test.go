package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/composition"
)

// projNoopTxRunner is a minimal persistence.TxRunner so the test PGProvider's
// TxManager() returns a non-nil runner; it never runs a real transaction.
type projNoopTxRunner struct{}

func (projNoopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// TestProjectionRuntimeOptions_MemoryMode verifies that without a postgres
// capability (memory mode) no projection options are wired — a projection
// declared in this mode then fails fast in the bootstrap phase6 drain, which is
// the intended behavior.
func TestProjectionRuntimeOptions_MemoryMode(t *testing.T) {
	t.Parallel()
	opts, err := projectionRuntimeOptions(&composition.SharedDeps{})
	require.NoError(t, err)
	assert.Nil(t, opts, "memory mode (shared.PG == nil) must wire no projection options")
}

// TestProjectionRuntimeOptions_PGMode verifies the PG-mode happy path constructs
// all four projection deps. The pool is non-connecting: MinConns defaults to 0 so
// NewWithConfig opens no connection, and the projection constructors only wrap the
// pool in pgexec (no query), so 127.0.0.1:1 is never dialed.
func TestProjectionRuntimeOptions_PGMode(t *testing.T) {
	t.Parallel()
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/gocell_unit")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer pool.Close()

	shared := &composition.SharedDeps{PG: capability.NewPGProvider(projNoopTxRunner{}, nil, pool)}
	opts, err := projectionRuntimeOptions(shared)
	require.NoError(t, err)
	assert.Len(t, opts, 4,
		"PG mode wires WithProjection{CheckpointStore,TxRunner,ReplaySource,Cursor}")
}
