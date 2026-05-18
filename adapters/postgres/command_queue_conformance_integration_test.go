//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

// TestPGCommandQueue_Conformance enrolls the PG command_queue implementation
// in the shared command.Queue + ActiveScanner conformance suite. The same
// suite drives the InMemQueue implementation in kernel/command/commandtest;
// both must pass identically.
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B
func TestPGCommandQueue_Conformance(t *testing.T) {
	commandtest.RunQueueConformance(t, pgCommandQueueFactory(t), commandtest.Features{
		RequiresAmbientTx:    true,
		SupportsLeaseRenewal: true,
	})
}

func pgCommandQueueFactory(t *testing.T) commandtest.QueueFactory {
	t.Helper()
	return func(t *testing.T) (command.Queue, command.ActiveScanner, commandtest.TxRunner, func() time.Time, func()) {
		t.Helper()
		q, txRunner, cleanup := setupPGCommandQueue(t)
		return q, q, txRunner, time.Now, cleanup
	}
}

// setupPGCommandQueue spins a fresh testcontainer, applies all migrations,
// seeds a device row (commands.device_id FK targets it) and returns the
// queue + TxManager. The FK seed is required because the conformance suite
// uses synthetic device IDs like "dev-a" / "dev-x" / "dev-y" / "dev-z";
// without seeding, every Enqueue would hit a foreign-key violation.
func setupPGCommandQueue(t *testing.T) (*PGCommandQueue, *TxManager, func()) {
	t.Helper()
	pool, basicCleanup := setupPostgres(t)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	// Seed the device IDs the conformance suite uses, so commands FK target
	// resolves. Kept in sync with the literals in
	// kernel/command/commandtest/conformance.go.
	seedDeviceIDs := []string{"dev-a", "dev-b", "dev-x", "dev-y", "dev-z"}
	for _, id := range seedDeviceIDs {
		_, err := pool.DB().Exec(ctx,
			`INSERT INTO devices (id, name, status, last_seen) VALUES ($1, $2, 'online', now())`,
			id, id)
		require.NoError(t, err, "seed device %q", id)
	}

	txMgr := NewTxManager(pool)
	q, err := NewCommandQueue(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		basicCleanup()
	}

	return q, txMgr, cleanup
}
