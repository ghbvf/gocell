package mem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// TestNewBundle_FourPrimitivesNonNil verifies that NewBundle hands back a
// fully populated quadruple — the four accessor methods never expose nil to
// composition roots, which is the only thing corecells/accesscore.WithMemBundle
// relies on at wire time.
func TestNewBundle_FourPrimitivesNonNil(t *testing.T) {
	b := NewBundle(clock.Real())

	assert.NotNil(t, b.UserRepository(), "UserRepository must be wired")
	assert.NotNil(t, b.RoleRepository(), "RoleRepository must be wired")
	assert.NotNil(t, b.SetupLock(), "SetupLock must be wired (NoopSetupLock for mem)")
	assert.NotNil(t, b.TxRunner(), "TxRunner must be wired (store-paired)")
}

// TestNewBundle_NoopSetupLockAcquire verifies the mem-mode noopSetupLock
// always returns nil. Acquire is wired by NewBundle and consumed via the
// SetupLock() accessor — actual serialization happens inside
// memTxRunner.RunInTx, which holds store.mu for the closure.
func TestNewBundle_NoopSetupLockAcquire(t *testing.T) {
	b := NewBundle(clock.Real())

	require.NoError(t, b.SetupLock().Acquire(context.Background()),
		"mem-mode SetupLock must be a no-op (store.mu serializes via TxRunner)")
}

// TestNewBundle_TxRunnerServializesStoreMutations verifies that the TxRunner
// returned by NewBundle is the store-paired one — running a closure ends with
// no error path even with a nil-handler closure, and the closure actually
// executes.
func TestNewBundle_TxRunnerExecutesClosure(t *testing.T) {
	b := NewBundle(clock.Real())

	executed := false
	err := b.TxRunner().RunInTx(context.Background(), func(context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, executed, "TxRunner.RunInTx must invoke the closure")
}
