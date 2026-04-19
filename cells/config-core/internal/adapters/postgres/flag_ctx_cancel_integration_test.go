//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlagWrite_CtxCancel_PGTxRollback verifies that when the context is
// cancelled before RunInTx executes, the PG transaction is rolled back and
// no row is inserted into feature_flags.
//
// This test uses a real PostgreSQL container (testcontainers-go) to confirm
// that the in-memory rollback semantics tested in ctx_cancel_test.go are
// consistent with real PG behaviour.
//
// Run with: go test -tags=integration -run TestFlagWrite_CtxCancel_PGTxRollback ./cells/config-core/internal/adapters/postgres/...
func TestFlagWrite_CtxCancel_PGTxRollback(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Setenv("CI", "1")
	}

	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()

	ctx := context.Background()
	key := "ctx-cancel-pg-" + uuid.NewString()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:          uuid.NewString(),
		Key:         key,
		Enabled:     true,
		Description: "ctx cancel PG test",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Create a context that is already cancelled.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately

	// Attempt to Create inside a RunInTx with a pre-cancelled context.
	// A real PG txMgr detects the cancelled context and does not commit,
	// returning context.Canceled.
	err := txMgr.RunInTx(cancelledCtx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	})
	require.Error(t, err, "RunInTx with cancelled ctx must return error")
	assert.ErrorIs(t, err, context.Canceled, "error must be context.Canceled")

	// Verify the row was NOT inserted — the transaction must have been rolled back.
	_, getErr := repo.GetByKey(ctx, key)
	require.Error(t, getErr, "flag must not exist in PG after cancelled tx rollback")
}

// TestFlagWrite_UpdateCtxCancel_PGTxRollback verifies that an Update inside a
// cancelled-context transaction does not persist changes to PG.
func TestFlagWrite_UpdateCtxCancel_PGTxRollback(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Setenv("CI", "1")
	}

	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()

	ctx := context.Background()
	key := "ctx-cancel-update-pg-" + uuid.NewString()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:          uuid.NewString(),
		Key:         key,
		Enabled:     false,
		Description: "original",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Insert the flag successfully first.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	}))

	// Attempt Update with a pre-cancelled context.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := txMgr.RunInTx(cancelledCtx, func(txCtx context.Context) error {
		_, err := repo.Update(txCtx, key, true, 50, "should not apply")
		return err
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	// Verify the original values are intact.
	got, getErr := repo.GetByKey(ctx, key)
	require.NoError(t, getErr)
	assert.False(t, got.Enabled, "enabled must not have changed after rollback")
	assert.Equal(t, "original", got.Description, "description must not have changed after rollback")
	assert.Equal(t, 1, got.Version, "version must not have been incremented after rollback")
}

// TestFlagWrite_DeleteCtxCancel_PGTxRollback verifies that a Delete inside a
// cancelled-context transaction does not remove the row from PG.
func TestFlagWrite_DeleteCtxCancel_PGTxRollback(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Setenv("CI", "1")
	}

	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()

	ctx := context.Background()
	key := "ctx-cancel-delete-pg-" + uuid.NewString()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:          uuid.NewString(),
		Key:         key,
		Enabled:     true,
		Description: "delete cancel test",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Insert the flag successfully first.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	}))

	// Attempt Delete with a pre-cancelled context.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := txMgr.RunInTx(cancelledCtx, func(txCtx context.Context) error {
		_, err := repo.Delete(txCtx, key)
		return err
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	// Verify the flag still exists.
	got, getErr := repo.GetByKey(ctx, key)
	require.NoError(t, getErr, "flag must still exist after cancelled Delete rollback")
	assert.Equal(t, key, got.Key)
}

// TestFlagWrite_TxRollback_OnRepoError verifies that when an operation inside
// RunInTx returns an error, the transaction is rolled back and no partial
// writes are visible.
func TestFlagWrite_TxRollback_OnRepoError(t *testing.T) {
	repo, txMgr, cleanup := setupFlagPG(t)
	defer cleanup()

	ctx := context.Background()
	key := "tx-rollback-test-" + uuid.NewString()

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:          uuid.NewString(),
		Key:         key,
		Enabled:     false,
		Description: "rollback on error test",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Insert the flag.
	require.NoError(t, txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		return repo.Create(txCtx, flag)
	}))

	// Simulate an error after the update (e.g. outbox write failure).
	simulatedErr := context.DeadlineExceeded
	err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		if _, err := repo.Update(txCtx, key, true, 75, "should rollback"); err != nil {
			return err
		}
		return simulatedErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, simulatedErr)

	// The Update must have been rolled back.
	got, getErr := repo.GetByKey(ctx, key)
	require.NoError(t, getErr)
	assert.False(t, got.Enabled, "enabled must not have changed after tx rollback")
	assert.Equal(t, 1, got.Version, "version must not have been incremented after tx rollback")
	assert.Equal(t, "rollback on error test", got.Description, "description must be original after tx rollback")

	_ = adapterpg.NewTxManager // ensure import used
}
