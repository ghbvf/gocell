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

// colocatedPGSet wraps prov as the sole pool serving configcore — the colocated
// shape projectionRuntimeOptions resolves via shared.PG.Sole().
func colocatedPGSet(t *testing.T, prov capability.PGProvider) capability.PGSet {
	t.Helper()
	set, err := capability.NewPGSet([]capability.PGInstance{{Provider: prov, Cells: []string{"configcore"}}})
	require.NoError(t, err)
	return set
}

// splitPGSet wraps two distinct providers (N>1 pools) so Sole() returns ok=false —
// the split shape projectionRuntimeOptions must fail closed on.
func splitPGSet(t *testing.T) capability.PGSet {
	t.Helper()
	set, err := capability.NewPGSet([]capability.PGInstance{
		{Provider: capability.NewPGProvider(projNoopTxRunner{}, nil, nil), Cells: []string{"accesscore"}},
		{Provider: capability.NewPGProvider(projNoopTxRunner{}, nil, nil), Cells: []string{"auditcore"}},
	})
	require.NoError(t, err)
	return set
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

// TestProjectionRuntimeOptions_PGMode verifies the fail-closed gate: in PG mode the
// durable projection_events journal source is NOT wired by default (the production
// posture stays fail-closed — a projection declared in PG mode then fails fast in the
// bootstrap phase6 drain) and is only wired under the explicit gate opt-in. The gated
// source is production-safe; the gate's removal (production-default flip) lands in PR-04
// (#1771), gated on T-06-2 e2e. The pool is non-connecting: MinConns defaults to 0 so
// NewWithConfig opens no connection, and the projection constructor only wraps the pool
// in pgexec (no query), so 127.0.0.1:1 is never dialed.
func TestProjectionRuntimeOptions_PGMode(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/gocell_unit")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	shared := &composition.SharedDeps{PG: colocatedPGSet(t, capability.NewPGProvider(projNoopTxRunner{}, nil, pool))}

	t.Run("gated off by default (fail-closed posture)", func(t *testing.T) {
		// No GOCELL_PROJECTION_PG_JOURNAL_PREVIEW → not wired; a projection declared
		// in PG mode then fails fast in the bootstrap drain.
		t.Setenv(envProjectionPGJournalPreview, "")
		opts, err := projectionRuntimeOptions(shared)
		require.NoError(t, err)
		assert.Nil(t, opts, "PG mode must NOT wire the durable journal source by default (fail-closed gate)")
	})

	t.Run("gate opt-in wires the source + readyz probe", func(t *testing.T) {
		t.Setenv(envProjectionPGJournalPreview, "true")
		opts, err := projectionRuntimeOptions(shared)
		require.NoError(t, err)
		// 5 options: WithProjection{CheckpointStore,TxRunner,ReplaySource,Cursor} +
		// WithHealthChecker(projection_journal_ready). The durable source is wired as
		// BOTH ReplaySource and Cursor (one instance), with the journal readyz probe
		// registered alongside.
		assert.Len(t, opts, 5,
			"gate opt-in wires WithProjection{CheckpointStore,TxRunner,ReplaySource,Cursor} + WithHealthChecker")
	})
}

// TestProjectionRuntimeOptions_SplitTopology_FailsClosed verifies the #2341 split
// guard: with the gate ON but N>1 pools (Sole() == false), the projection harness
// MUST fail closed — each pool has its own projection_events with an incomparable
// global_seq, so wiring a single source would corrupt the read model. The reject
// happens BEFORE any pool I/O (Sole() short-circuits), so no real pool is needed.
func TestProjectionRuntimeOptions_SplitTopology_FailsClosed(t *testing.T) {
	t.Setenv(envProjectionPGJournalPreview, "true")
	shared := &composition.SharedDeps{PG: splitPGSet(t)}
	opts, err := projectionRuntimeOptions(shared)
	require.Error(t, err, "split topology (N pools) must fail closed when the projection gate is on")
	assert.Nil(t, opts)
	assert.Contains(t, err.Error(), "split topology",
		"error should explain the split-topology incompatibility")
}
