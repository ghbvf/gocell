package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestAssertAmbientTx_NoTx verifies that assertAmbientTx returns an
// errcode.ErrInternal error when no ambient transaction is present in ctx.
func TestAssertAmbientTx_NoTx(t *testing.T) {
	err := assertAmbientTx(context.Background())
	require.Error(t, err, "assertAmbientTx must return error when no tx in ctx")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be an *errcode.Error")
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, ec.Message, "ambient transaction",
		"error message must mention ambient transaction")
}

// TestAssertAmbientTx_WithTx verifies that assertAmbientTx returns nil when
// a valid pgx.Tx is present in ctx under persistence.TxCtxKey.
func TestAssertAmbientTx_WithTx(t *testing.T) {
	// fakeTx satisfies pgx.Tx via the type-assert in assertAmbientTx. We only
	// need to store something that the type assertion passes; none of the
	// interface methods are called.
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, fakeTx{})
	err := assertAmbientTx(ctx)
	assert.NoError(t, err, "assertAmbientTx must return nil when tx present in ctx")
}

// TestGetByIDForUpdate_NoAmbientTx verifies that PGUserRepo.GetByIDForUpdate
// returns errcode.ErrInternal when called without an ambient transaction,
// before any DB call is made (pool can be nil — guard fires first).
func TestGetByIDForUpdate_NoAmbientTx(t *testing.T) {
	repo := &PGUserRepo{db: pgExecutor{pool: nil}}

	_, err := repo.GetByIDForUpdate(context.Background(), "some-id")
	require.Error(t, err, "GetByIDForUpdate must error without ambient tx")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, ec.Message, "ambient transaction")
}

// TestGetByUsernameForUpdate_NoAmbientTx verifies that
// PGUserRepo.GetByUsernameForUpdate returns errcode.ErrInternal when called
// without an ambient transaction, before any DB call is made.
func TestGetByUsernameForUpdate_NoAmbientTx(t *testing.T) {
	repo := &PGUserRepo{db: pgExecutor{pool: nil}}

	_, err := repo.GetByUsernameForUpdate(context.Background(), "alice")
	require.Error(t, err, "GetByUsernameForUpdate must error without ambient tx")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, ec.Message, "ambient transaction")
}

// TestBumpAuthzEpoch_NoAmbientTx verifies that PGUserRepo.BumpAuthzEpoch
// returns errcode.ErrInternal when called without an ambient transaction,
// before any DB call is made (pool can be nil — guard fires first).
func TestBumpAuthzEpoch_NoAmbientTx(t *testing.T) {
	repo := &PGUserRepo{db: pgExecutor{pool: nil}}

	_, err := repo.BumpAuthzEpoch(context.Background(), "usr-test-001")
	require.Error(t, err, "BumpAuthzEpoch must error without ambient tx")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, ec.Message, "ambient transaction")
}

// fakeTx is a minimal stub that satisfies pgx.Tx for type-assertion purposes.
// Only the interface embedding is required; no method bodies are exercised.
type fakeTx struct{ pgx.Tx }
