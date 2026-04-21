//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/stretchr/testify/require"
)

func TestPGRefreshStore_ContractSuite(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	// Run migrations using a dedicated tracking table so this test's schema
	// version tracking does not collide with other integration test suites.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_refresh_store")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		t.Helper()
		// No TRUNCATE: storetest assigns unique session IDs per test case
		// ("sess-1".."sess-13b") and unique random tokens, so parallel subtests
		// do not collide. The container is fresh per TestPGRefreshStore_ContractSuite
		// invocation, so stale rows from a previous run are not a concern.

		clock := storetest.NewFakeClock(baseTime)
		store := NewRefreshStore(pool.DB(), policy, clock, nil)
		return store, clock
	})
}
