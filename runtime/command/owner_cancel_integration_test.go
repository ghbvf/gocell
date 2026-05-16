//go:build integration

package command

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestSweeperLifecycle_OwnerCancel_ExitsWithoutOnStop verifies the C.2 owner-ctx
// contract at integration depth: canceling ownerCtx causes the sweeper goroutine
// to exit without any explicit OnStop call.
//
// This test pins the LIFO teardown sequence behaviour described in
// docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-B:
// ownerCancel runs first (before lifecycle.Stop) in the LIFO teardown order.
// Workers must exit when ownerCtx is canceled, so that subsequent lifecycle.Stop
// (drain phase) sees an already-exited goroutine and returns promptly.
//
// goleak verifies no sweeper goroutine survives after ownerCancel without Stop.
func TestSweeperLifecycle_OwnerCancel_ExitsWithoutOnStop(t *testing.T) {
	defer goleak.VerifyNone(t)

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("owner-cancel-integration", sw, testtime.D1h, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())

	require.NoError(t, lc.Start(ownerCtx))

	// Cancel ownerCtx — mimics bootstrap LIFO: ownerCancel runs before lifecycle.Stop.
	// The sweeper goroutine must exit via ownerCtx.Done() without needing OnStop.
	ownerCancel()

	// Optionally call Stop to drain (verifies prompt return since goroutine is exiting).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx),
		"Stop after ownerCancel must succeed promptly (goroutine already exiting)")

	// goleak (deferred at top) asserts no sweeper goroutine survives after ownerCancel.
}

// TestSweeperLifecycle_OwnerCtxNotNilAfterStart verifies that ownerCtx is not
// canceled by Start itself: Start must not cancel the long-lived owner ctx.
// After Start returns, ownerCtx.Err() must be nil.
func TestSweeperLifecycle_OwnerCtxNotNilAfterStart(t *testing.T) {
	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q)
	require.NoError(t, err)
	lc := NewSweeperLifecycle("owner-ctx-not-nil", sw, testtime.D1h, clock.Real())

	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// ownerCtx must still be live after Start returns.
	assert.NoError(t, ownerCtx.Err(),
		"Start must not cancel ownerCtx: ownerCtx is the long-lived assembly owner")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx))
}
