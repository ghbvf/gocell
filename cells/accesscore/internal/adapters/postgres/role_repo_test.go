package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestPGRoleRepo_Constructor_FailFast verifies that NewPGRoleRepo rejects nil
// dependencies at construction time, without requiring a real PG connection.
// Uses new(pgxpool.Pool) / new(adapterpg.TxManager) (non-nil zero values) to
// reach the later guards — mirroring the session_store_uuid_test.go pattern.
func TestPGRoleRepo_Constructor_FailFast(t *testing.T) {
	fakePool := new(pgxpool.Pool)       // non-nil zero value, no real PG needed
	fakeTxm := new(adapterpg.TxManager) // non-nil zero value, reaches clock guard

	assertValidationFailed := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	}

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewPGRoleRepo(nil, fakeTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_txRunner_typed_nil", func(t *testing.T) {
		var nilTxm *adapterpg.TxManager // typed-nil caught by validation.IsNilInterface
		_, err := NewPGRoleRepo(fakePool, nilTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_clock_typed_nil", func(t *testing.T) {
		_, err := NewPGRoleRepo(fakePool, fakeTxm, nil)
		assertValidationFailed(t, err)
	})
}
