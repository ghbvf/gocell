//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// TestPGCommandQueue_Conformance enrolls the PG command_queue implementation
// in the shared command.Queue + ActiveScanner conformance suite. The same
// suite drives the InMemQueue implementation in kernel/command/commandtest;
// both must pass identically.
//
// One shared per-test DB (migratedPool) is reused across all sub-tests; each
// sub-test factory call TRUNCATEs `commands` and re-seeds the device FK
// targets to give a pristine schema view without paying per-sub-test DB
// creation overhead (33 sub-tests × DB cost → flaky on constrained CI runners).
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B
func TestPGCommandQueue_Conformance(t *testing.T) {
	testutil.RequireDocker(t)
	pool := migratedPool(t)
	txMgr := NewTxManager(pool)

	factory := func(t *testing.T) (command.Queue, command.ActiveScanner, commandtest.TxRunner, func() time.Time, func()) {
		t.Helper()
		resetCommandQueueSchema(t, pool)
		q, err := NewCommandQueue(pool.DB(), txMgr, clock.Real())
		require.NoError(t, err)
		return q, q, txMgr, time.Now, func() {} // shared pool — no per-sub-test teardown
	}

	commandtest.RunQueueConformance(t, factory, commandtest.Features{
		// PGCommandQueue self-wraps each mutating method in RunInTx, so the
		// suite exercises the production caller path with RequiresAmbientTx=false.
		RequiresAmbientTx:    false,
		SupportsLeaseRenewal: true,
	})
}

// resetCommandQueueSchema truncates the commands table and re-seeds the
// devices rows that the conformance suite Enqueue path FK-targets. CASCADE
// on devices clears any leftover commands, then we re-insert the canonical
// seed set. Kept in lock-step with the literals in
// kernel/command/commandtest/conformance.go.
func resetCommandQueueSchema(t *testing.T, pool *Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.DB().Exec(ctx, "TRUNCATE TABLE commands, devices RESTART IDENTITY CASCADE")
	require.NoError(t, err, "truncate commands/devices")

	for _, id := range []string{"dev-a", "dev-b", "dev-x", "dev-y", "dev-z"} {
		_, err := pool.DB().Exec(ctx,
			`INSERT INTO devices (id, name, status, last_seen) VALUES ($1, $2, 'online', now())`,
			id, id)
		require.NoError(t, err, "seed device %q", id)
	}
}

// TestPGCommandQueue_RepoReadinessConformance wires PGCommandQueue through the
// shared RepoHealthProber conformance harness:
//   - healthy: a fully migrated queue returns nil from RepoReady.
//   - broken: a queue whose commands table has been dropped returns non-nil.
//
// Each instance uses an isolated per-test DB (migratedPool) so the DROP TABLE
// for the broken scenario does not affect the healthy pool's commands table.
func TestPGCommandQueue_RepoReadinessConformance(t *testing.T) {
	testutil.RequireDocker(t)
	ctx := context.Background()

	// healthy: per-test DB with full migrations — commands table intact.
	healthyPool := migratedPool(t)
	healthyTxm := NewTxManager(healthyPool)
	healthy, err := NewCommandQueue(healthyPool.DB(), healthyTxm, clock.Real())
	require.NoError(t, err)

	// broken: per-test DB with migrations applied, then commands table dropped
	// to simulate schema drift / missing migration.
	brokenPool := migratedPool(t)
	_, dropErr := brokenPool.DB().Exec(ctx, "DROP TABLE IF EXISTS commands CASCADE")
	require.NoError(t, dropErr, "drop commands table for broken scenario")
	brokenTxm := NewTxManager(brokenPool)
	broken, err := NewCommandQueue(brokenPool.DB(), brokenTxm, clock.Real())
	require.NoError(t, err)

	celltest.RunRepoReadinessConformance(t, "command-queue-pg", healthy, broken)
}
