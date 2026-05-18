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
// One PG testcontainer is shared across all sub-tests; each sub-test factory
// call TRUNCATEs `commands` and re-seeds the device FK targets to give a
// pristine schema view without paying the ~1.5s container-start cost per
// sub-test (33 sub-tests × ~1.5s = 50s + Docker daemon contention → flaky on
// constrained CI runners).
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B
func TestPGCommandQueue_Conformance(t *testing.T) {
	testutil.RequireDocker(t)
	pool, txMgr, terminate := setupSharedCommandQueuePG(t)
	t.Cleanup(terminate)

	factory := func(t *testing.T) (command.Queue, command.ActiveScanner, commandtest.TxRunner, func() time.Time, func()) {
		t.Helper()
		resetCommandQueueSchema(t, pool)
		q, err := NewCommandQueue(pool.DB(), txMgr, clock.Real())
		require.NoError(t, err)
		return q, q, txMgr, time.Now, func() {} // shared container — no per-sub-test teardown
	}

	commandtest.RunQueueConformance(t, factory, commandtest.Features{
		// PGCommandQueue self-wraps each mutating method in RunInTx, so the
		// suite exercises the production caller path with RequiresAmbientTx=false.
		RequiresAmbientTx:    false,
		SupportsLeaseRenewal: true,
	})
}

// setupSharedCommandQueuePG spins ONE testcontainer + migrates schema +
// creates a TxManager that the whole conformance suite reuses. terminate
// closes the pool and stops the container in t.Cleanup.
func setupSharedCommandQueuePG(t *testing.T) (*Pool, *TxManager, func()) {
	t.Helper()
	pool, basicCleanup := setupPostgres(t)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	return pool, NewTxManager(pool), basicCleanup
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
// Each instance uses an isolated schema pool so the DROP TABLE for the broken
// scenario does not affect the healthy pool's commands table.
func TestPGCommandQueue_RepoReadinessConformance(t *testing.T) {
	testutil.RequireDocker(t)
	base, baseTeardown := setupPostgres(t)
	t.Cleanup(baseTeardown)

	ctx := context.Background()

	// healthy: isolated schema with full migrations — commands table intact.
	healthyPool := isolatedSchemaPool(t, ctx, base)
	t.Cleanup(func() { _ = healthyPool.Close(context.Background()) })
	healthyMigrator, err := NewMigrator(healthyPool, testMigrationsFS(t), "schema_migrations_readyz_cq_healthy")
	require.NoError(t, err)
	require.NoError(t, healthyMigrator.Up(ctx))
	healthyTxm := NewTxManager(healthyPool)
	healthy, err := NewCommandQueue(healthyPool.DB(), healthyTxm, clock.Real())
	require.NoError(t, err)

	// broken: isolated schema with migrations applied, then commands table dropped
	// to simulate schema drift / missing migration.
	brokenPool := isolatedSchemaPool(t, ctx, base)
	t.Cleanup(func() { _ = brokenPool.Close(context.Background()) })
	brokenMigrator, err := NewMigrator(brokenPool, testMigrationsFS(t), "schema_migrations_readyz_cq_broken")
	require.NoError(t, err)
	require.NoError(t, brokenMigrator.Up(ctx))
	_, dropErr := brokenPool.DB().Exec(ctx, "DROP TABLE IF EXISTS commands CASCADE")
	require.NoError(t, dropErr, "drop commands table for broken scenario")
	brokenTxm := NewTxManager(brokenPool)
	broken, err := NewCommandQueue(brokenPool.DB(), brokenTxm, clock.Real())
	require.NoError(t, err)

	celltest.RunRepoReadinessConformance(t, "command-queue-pg", healthy, broken)
}
