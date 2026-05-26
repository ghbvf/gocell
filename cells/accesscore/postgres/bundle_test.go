package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewBundle_FailFast verifies the three nil-validation branches of
// accesspg.NewBundle. Mirrors the internal/adapters/postgres role_repo_test.go
// pattern: non-nil zero-value pool + outbox.DemoTxRunner reach the next guard
// without needing a real PG connection (outbox.DemoTxRunner is the canonical
// in-mem persistence.TxRunner; CELL-TEST-NO-ADAPTER-IMPORT-01).
func TestNewBundle_FailFast(t *testing.T) {
	fakePool := new(pgxpool.Pool)    // non-nil zero value, no real PG needed
	fakeTxm := outbox.DemoTxRunner{} // non-nil TxRunner, reaches clock guard

	assertValidationFailed := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	}

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewBundle(nil, fakeTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_txMgr_typed_nil", func(t *testing.T) {
		var nilTxm *outbox.DemoTxRunner
		_, err := NewBundle(fakePool, nilTxm, clock.Real())
		assertValidationFailed(t, err)
	})

	t.Run("nil_clock_typed_nil", func(t *testing.T) {
		_, err := NewBundle(fakePool, fakeTxm, nil)
		assertValidationFailed(t, err)
	})
}

// TestNewBundle_HappyPath wires the four primitives through the typed funnel
// using a zero-value pool — no real PG connection is needed because the
// inner constructors (NewPGUserRepo / NewPGRoleRepo / NewPGSetupLock) and
// newPGExecutor only store the pool reference. Verifies all four accessor
// methods return non-nil after a successful NewBundle call.
func TestNewBundle_HappyPath(t *testing.T) {
	fakePool := new(pgxpool.Pool)
	fakeTxm := outbox.DemoTxRunner{}

	b, err := NewBundle(fakePool, fakeTxm, clock.Real())
	require.NoError(t, err)

	assert.NotNil(t, b.UserRepository(), "UserRepository must be wired")
	assert.NotNil(t, b.RoleRepository(), "RoleRepository must be wired")
	assert.NotNil(t, b.SetupLock(), "SetupLock must be wired")
	assert.NotNil(t, b.TxRunner(), "TxRunner must be wired (WrapForCell-wrapped txMgr)")
}
