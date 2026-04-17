package main

import (
	"context"
	"os"
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardPublisher is a minimal outbox.Publisher for wiring tests.
// It discards all published messages without error.
type discardPublisher struct{}

func (discardPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = discardPublisher{}

// TestBuildConfigCoreOpts_InMemoryMode_NoRelay asserts that an unset
// GOCELL_CELL_ADAPTER_MODE returns a nil relay worker. No database connection
// is attempted; this test requires no external services.
//
// Regression guard for A11: if the relay is accidentally wired in memory mode
// it would try to Start() without a real DB and either panic or block.
func TestBuildConfigCoreOpts_InMemoryMode_NoRelay(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "")
	t.Setenv("GOCELL_PG_DSN", "") // ensure no PG connection is attempted

	ctx := context.Background()
	mode, _, pool, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, metrics.NopProvider{})

	require.NoError(t, err)
	assert.Equal(t, "memory", mode, "unset GOCELL_CELL_ADAPTER_MODE must resolve to memory")
	assert.Nil(t, pool, "in-memory mode must not open a PG pool")
	assert.Nil(t, relay, "in-memory mode must not create a relay worker")
}

// TestBuildConfigCoreOpts_ExplicitMemoryMode_NoRelay is the explicit counterpart:
// GOCELL_CELL_ADAPTER_MODE=memory must also produce no relay.
func TestBuildConfigCoreOpts_ExplicitMemoryMode_NoRelay(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
	t.Setenv("GOCELL_PG_DSN", "")

	ctx := context.Background()
	mode, _, pool, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, metrics.NopProvider{})

	require.NoError(t, err)
	assert.Equal(t, "memory", mode)
	assert.Nil(t, pool)
	assert.Nil(t, relay)
}

// TestBuildConfigCoreOpts_UnknownMode_Error asserts that an unrecognised
// GOCELL_CELL_ADAPTER_MODE returns an error and no relay worker.
func TestBuildConfigCoreOpts_UnknownMode_Error(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "cassandra")

	ctx := context.Background()
	_, _, _, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, metrics.NopProvider{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cassandra")
	assert.Nil(t, relay, "error path must not leak a relay worker")
}

// TestBuildConfigCoreOpts_PGMode_RelayNonNil asserts that postgres mode
// produces a non-nil relay worker that satisfies worker.Worker. This test
// requires a running PostgreSQL instance (GOCELL_PG_DSN must be set); it
// skips gracefully if the DSN is absent so it does not block unit test suites.
//
// The assertion that relayWorker != nil is the critical regression check for
// A11: before the fix, buildConfigCoreOpts never returned a relay, so the
// bootstrap could not register it.
func TestBuildConfigCoreOpts_PGMode_RelayNonNil(t *testing.T) {
	pgDSN, ok := os.LookupEnv("GOCELL_PG_DSN")
	if !ok || pgDSN == "" {
		t.Skip("GOCELL_PG_DSN not set; skipping PG-mode relay wiring test")
	}

	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")

	ctx := context.Background()
	mode, opts, pool, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, metrics.NopProvider{})

	require.NoError(t, err, "postgres mode must not error when DSN is valid")
	assert.Equal(t, "postgres", mode)
	assert.NotNil(t, pool, "postgres mode must open a PG pool")
	assert.NotEmpty(t, opts, "postgres mode must return cell options")
	require.NotNil(t, relay, "postgres mode must return a relay worker (A11 fix)")

	// relay is typed as worker.Worker — the assignment itself is the compile-time
	// interface check. No explicit assertion needed.
	require.NotNil(t, relay)

	pool.Close()
}
