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
	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
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
		return repo.Create(txCtx, integrationTestTenantA, flag)
	}))

	// Simulate restart: create a brand-new FlagRepository (same pool/session,
	// same PG container) to verify no in-memory state is retained between
	// repository instances. In production, restarting the binary would create
	// a new pool+session pointed at the same PG — this mirrors that behaviour.
	repo2 := NewFlagRepository(repo.session, clock.Real())

	got, err := repo2.GetByKey(ctx, integrationTestTenantA, "restart.test.flag")
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
		return repo.Create(txCtx, integrationTestTenantA, flag)
	}))

	// Toggle enabled=true via tx; version becomes 2.
	var toggled *domain.FeatureFlag
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		toggled, err = repo.Toggle(txCtx, integrationTestTenantA, "toggle.persist.flag", 1, true)
		return err
	}))
	assert.Equal(t, 2, toggled.Version)
	assert.True(t, toggled.Enabled)

	// Re-read via a "new" repo instance to confirm persistence.
	repo2 := NewFlagRepository(repo.session, clock.Real())
	got, err := repo2.GetByKey(ctx, integrationTestTenantA, "toggle.persist.flag")
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version, "version must persist after toggle")
	assert.True(t, got.Enabled, "enabled state must persist after toggle")
	assert.Equal(t, 0, got.RolloutPercentage, "rollout_percentage must be unchanged by toggle")
}

// TestFlagRepo_Integration_CrossTenantIsolation verifies that feature flags
// written under testTenantA are invisible to testTenantB on every data method.
// Mirrors accesscore's conformCrossTenantIsolation pattern.
func TestFlagRepo_Integration_CrossTenantIsolation(t *testing.T) {
	repo, txMgr := setupFlagPG(t)
	ctx := context.Background()

	key := "cross-tenant-flag-" + uuid.NewString()
	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:          uuid.NewString(),
		Key:         key,
		Enabled:     true,
		Description: "tenant-a-only",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Write under tenant A.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, integrationTestTenantA, flag)
	}))

	t.Run("GetByKey_under_tenantB_returns_not_found", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, integrationTestTenantB, key)
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagNotFound, ec.Code)
	})

	t.Run("GetByKey_under_tenantA_succeeds", func(t *testing.T) {
		got, err := repo.GetByKey(ctx, integrationTestTenantA, key)
		require.NoError(t, err)
		assert.Equal(t, "tenant-a-only", got.Description)
	})

	t.Run("Update_under_tenantB_returns_not_found", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Update(txCtx, integrationTestTenantB, key, 1, false, 0, "should-not-apply")
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagNotFound, ec.Code)
	})

	t.Run("Delete_under_tenantB_returns_not_found", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Delete(txCtx, integrationTestTenantB, key, 1)
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagNotFound, ec.Code)
	})

	t.Run("List_under_tenantB_returns_empty_for_key", func(t *testing.T) {
		flags, err := repo.List(ctx, integrationTestTenantB, query.ListParams{
			Limit: 50,
			Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
		})
		require.NoError(t, err)
		for _, f := range flags {
			assert.NotEqual(t, key, f.Key, "tenant B must not see tenant A's flag in List")
		}
	})

	t.Run("Toggle_under_tenantB_returns_not_found", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Toggle(txCtx, integrationTestTenantB, key, 1, false)
			return err
		})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrFlagNotFound, ec.Code)
	})
}

// TestFlagRepo_Integration_EmptyTenantGuard verifies that every data method
// rejects a zero/empty tenant.TenantID with a validation error rather than
// silently issuing a tenant-less query.
func TestFlagRepo_Integration_EmptyTenantGuard(t *testing.T) {
	repo, txMgr := setupFlagPG(t)
	ctx := context.Background()
	zero := tenant.TenantID("") // intentionally invalid

	assertValidationErr := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "empty tenant must return error")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
			"empty tenant must return ErrValidationFailed, not a DB error")
	}

	t.Run("Create_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			return repo.Create(txCtx, zero, &domain.FeatureFlag{Key: "k"})
		})
		assertValidationErr(t, err)
	})

	t.Run("GetByKey_zero_tenant", func(t *testing.T) {
		_, err := repo.GetByKey(ctx, zero, "k")
		assertValidationErr(t, err)
	})

	t.Run("Update_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Update(txCtx, zero, "k", 1, false, 0, "")
			return err
		})
		assertValidationErr(t, err)
	})

	t.Run("Delete_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Delete(txCtx, zero, "k", 1)
			return err
		})
		assertValidationErr(t, err)
	})

	t.Run("List_zero_tenant", func(t *testing.T) {
		_, err := repo.List(ctx, zero, query.ListParams{Limit: 10})
		assertValidationErr(t, err)
	})

	t.Run("Toggle_zero_tenant", func(t *testing.T) {
		err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := repo.Toggle(txCtx, zero, "k", 1, true)
			return err
		})
		assertValidationErr(t, err)
	})
}
