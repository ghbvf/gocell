package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/capability"
	"github.com/ghbvf/gocell/framework/runtime/composition"
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

// TestProjectionRuntimeOptions_PGMode verifies the production-default wiring after
// the #1771 gate removal: with a projection actually declared
// (generatedProjectionSourceTopics non-empty — accesscore session_registry), PG mode
// wires the durable projection_events source by default. The former
// GOCELL_PROJECTION_PG_JOURNAL_PREVIEW fail-closed gate is gone (EPIC #1504 PR-04):
// the durable source is production-safe, not an opt-in preview. The pool is
// non-connecting: MinConns defaults to 0 so NewWithConfig opens no connection, and the
// projection constructor only wraps the pool in pgexec (no query), so 127.0.0.1:1 is
// never dialed.
func TestProjectionRuntimeOptions_PGMode(t *testing.T) {
	t.Parallel()
	// Anti-vacuity precondition: this test exercises the wired-when-declared branch
	// only if a projection is actually declared. Pin it so a future regen that drops
	// every projection (empty topics) turns this red instead of silently asserting the
	// no-projection branch.
	require.NotEmpty(t, generatedProjectionSourceTopics(),
		"precondition: at least one projection must be declared for the wired branch")

	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/gocell_unit")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	shared := &composition.SharedDeps{PG: capability.NewPGProvider(projNoopTxRunner{}, nil, pool)}

	opts, err := projectionRuntimeOptions(shared)
	require.NoError(t, err)
	// 5 options: WithProjection{CheckpointStore,TxRunner,ReplaySource,Cursor} +
	// WithHealthChecker(projection_journal_ready). The durable source is wired as
	// BOTH ReplaySource and Cursor (one instance), with the journal readyz probe
	// registered alongside.
	assert.Len(t, opts, 5,
		"PG mode with a declared projection wires WithProjection{CheckpointStore,TxRunner,ReplaySource,Cursor} + WithHealthChecker")
}
