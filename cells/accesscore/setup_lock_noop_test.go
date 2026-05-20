package accesscore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
)

// TestNoopSetupLock_Acquire verifies the mem-mode NoopSetupLock returns nil
// — its sole purpose is satisfying the mandatory WithSetupLock contract
// while the actual serialization lives in memTxRunner.RunInTx (which holds
// store.mu for the whole closure). Compile-time interface check is in
// setup_lock_noop.go; this test exercises the runtime contract.
func TestNoopSetupLock_Acquire(t *testing.T) {
	var lock ports.SetupLockAcquirer = NoopSetupLock{}
	require.NoError(t, lock.Acquire(context.Background()))

	// Repeated Acquire calls remain no-ops — there is no internal state.
	assert.NoError(t, lock.Acquire(context.Background()))
}
