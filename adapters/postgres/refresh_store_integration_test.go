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
	// Run migrations
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		t.Helper()
		_, err := pool.DB().Exec(ctx, "TRUNCATE refresh_tokens")
		require.NoError(t, err)

		clock := storetest.NewFakeClock(baseTime)
		store := NewRefreshStore(pool.DB(), policy, clock, nil)
		return store, clock
	})
}
