//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
)

// setupFlagPG clones the package-shared pre-migrated template database
// into a fresh per-test database and returns a FlagRepository wired over
// it. Pool + per-test DB lifecycle is owned by t.Cleanup inside
// sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupFlagPG(t *testing.T) (*FlagRepository, *adapterpg.TxManager) {
	t.Helper()

	pool := sharedPG.NewPerTestPool(t)
	session := NewSession(pool.DB())
	repo := NewFlagRepository(session, clock.Real())
	txMgr := adapterpg.NewTxManager(pool)

	return repo, txMgr
}

// TestFlagRepo_Restart_Persistence verifies that a flag created in one
// FlagRepository instance is visible after the repository is recreated
// (simulating a process restart with the same PG container).
func TestFlagRepo_Restart_Persistence(t *testing.T) {
	repo, txMgr := setupFlagPG(t)
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
	repo2 := NewFlagRepository(repo.session, clock.Real())

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
	repo, txMgr := setupFlagPG(t)
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
		toggled, err = repo.Toggle(txCtx, "toggle.persist.flag", 1, true)
		return err
	}))
	assert.Equal(t, 2, toggled.Version)
	assert.True(t, toggled.Enabled)

	// Re-read via a "new" repo instance to confirm persistence.
	repo2 := NewFlagRepository(repo.session, clock.Real())
	got, err := repo2.GetByKey(ctx, "toggle.persist.flag")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version, "version must persist after toggle")
	assert.True(t, got.Enabled, "enabled state must persist after toggle")
	assert.Equal(t, 0, got.RolloutPercentage, "rollout_percentage must be unchanged by toggle")
}
