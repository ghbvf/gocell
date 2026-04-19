//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupFlagPG spins up a PostgreSQL container, applies all migrations
// (001-007), and returns a FlagRepository backed by a Session, TxManager,
// and cleanup func.
func setupFlagPG(t *testing.T) (*FlagRepository, *adapterpg.TxManager, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	session := NewSession(pool.DB())
	repo := NewFlagRepository(session)
	txMgr := adapterpg.NewTxManager(pool)

	cleanup := func() {
		pool.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return repo, txMgr, cleanup
}

// TestFlagRepo_Restart_Persistence verifies that a flag created in one
// FlagRepository instance is visible after the repository is recreated
// (simulating a process restart with the same PG container).
func TestFlagRepo_Restart_Persistence(t *testing.T) {
	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:                uuid.NewString(),
		Key:               "restart.test.flag",
		Enabled:           true,
		RolloutPercentage: 25,
		Description:       "restart test",
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	// Write via tx.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	}))

	// Simulate restart: create a brand-new FlagRepository (same pool/session,
	// same PG container) to verify no in-memory state is retained between
	// repository instances. In production, restarting the binary would create
	// a new pool+session pointed at the same PG — this mirrors that behaviour.
	repo2 := NewFlagRepository(repo.session)

	got, err := repo2.GetByKey(ctx, "restart.test.flag")
	require.NoError(t, err)
	assert.Equal(t, flag.ID, got.ID)
	assert.True(t, got.Enabled)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, 25, got.RolloutPercentage)
	assert.Equal(t, "restart test", got.Description)
}

// TestFlagRepo_Toggle_Persistence verifies that Toggle increments version and
// the updated version persists in PG (survives repository re-creation).
func TestFlagRepo_Toggle_Persistence(t *testing.T) {
	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:                uuid.NewString(),
		Key:               "toggle.persist.flag",
		Enabled:           false,
		RolloutPercentage: 0,
		Description:       "toggle persist",
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	}))

	// Toggle enabled=true via tx; version becomes 2.
	var toggled *domain.FeatureFlag
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		toggled, err = repo.Toggle(txCtx, "toggle.persist.flag", true)
		return err
	}))
	assert.Equal(t, 2, toggled.Version)
	assert.True(t, toggled.Enabled)

	// Re-read via a "new" repo instance to confirm persistence.
	repo2 := NewFlagRepository(repo.session)
	got, err := repo2.GetByKey(ctx, "toggle.persist.flag")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version, "version must persist after toggle")
	assert.True(t, got.Enabled, "enabled state must persist after toggle")
	assert.Equal(t, 0, got.RolloutPercentage, "rollout_percentage must be unchanged by toggle")
}
