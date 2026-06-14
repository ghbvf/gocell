//go:build integration

package saga_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

// TestPGJournal_RepoReadinessConformance wires PGJournal through the shared
// RepoHealthProber conformance harness:
//   - healthy: a fully migrated journal returns nil from RepoReady.
//   - broken: a journal whose saga_instances table has been dropped returns non-nil.
//
// Each journal instance uses an isolated per-test DB (sharedPG.NewPerTestPool)
// so the DROP TABLE for the broken scenario does not affect the healthy pool's
// saga tables. The real *PGJournal (not a mock) is passed to
// RunRepoReadinessConformance so the archtest type-resolution detects coverage.
func TestPGJournal_RepoReadinessConformance(t *testing.T) {
	ctx := context.Background()
	clk := clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))

	// healthy: per-test DB with full migrations — saga tables intact.
	healthyPool := sharedPG.NewPerTestPool(t)
	healthy, err := saga.NewJournal(healthyPool.DB(), clk)
	require.NoError(t, err, "construct healthy PGJournal")

	// broken: per-test DB with migrations applied, then both saga tables dropped
	// (saga_events first to respect the FK, saga_instances next with CASCADE) to
	// simulate schema drift / missing migration. PGJournal.repoReadyQuery probes
	// both tables in a UNION ALL, so either missing table surfaces a non-nil error.
	brokenPool := sharedPG.NewPerTestPool(t)
	_, dropErr := brokenPool.DB().Exec(ctx, "DROP TABLE IF EXISTS saga_events, saga_instances CASCADE")
	require.NoError(t, dropErr, "drop saga tables for broken scenario")

	broken, err := saga.NewJournal(brokenPool.DB(), clk)
	require.NoError(t, err, "construct broken PGJournal")

	celltest.RunRepoReadinessConformance(t, "saga-pg", healthy, broken)
}
