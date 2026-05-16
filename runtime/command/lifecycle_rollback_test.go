package command

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

// rollbackStopBudget caps how long the rollback Stop call waits for the
// sweeper goroutine to exit. The fast-path (ticker has not fired) returns
// immediately; the slow path (ticker mid-tick) returns after the current
// SweepTick completes. 2s is generous for slow CI.
const rollbackStopBudget = 2 * time.Second

// TestSweeperLifecycle_StartupFailRollback simulates the bootstrap LIFO
// rollback scenario: cell A's sweeper.OnStart succeeds → cell B's OnStart
// fails → bootstrap reverses LIFO and calls A's OnStop. The sweeper
// goroutine MUST exit cleanly within the stop budget, leaving no
// background work running.
//
// C.2 owner-ctx contract: SweeperLifecycle.Start derives worker runCtx from
// the owner ctx (controller-runtime Runnable.Start semantics). This test
// pins the rollback path: bootstrap-orchestrated OnStop is the sole explicit
// worker-cancel path; the owner ctx also provides an implicit cancel.
//
// goleak verifies no sweeper goroutine survives past Stop.
func TestSweeperLifecycle_StartupFailRollback(t *testing.T) {
	defer goleak.VerifyNone(t)

	q := commandtest.NewInMemQueue()
	sw, err := kcommand.NewSweeper(q, q) // C.1: no clock arg
	require.NoError(t, err)
	lc := NewSweeperLifecycle("devicecommand.sweeper", sw, time.Hour, clock.Real()) // long interval — no tick fires

	// Step 1: simulate cell A startup completing (Start returns prompt).
	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	defer ownerCancel()

	require.NoError(t, lc.Start(ownerCtx))

	// Step 2: simulate bootstrap detecting cell B's OnStart failure and
	// reversing LIFO. Cell A's OnStop is the rollback step.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), rollbackStopBudget)
	defer stopCancel()
	require.NoError(t, lc.Stop(stopCtx),
		"rollback OnStop must terminate the sweeper goroutine within budget")

	// Step 3: idempotent Stop after rollback (defensive — bootstrap may call
	// Stop a second time during normal shutdown after rollback).
	require.NoError(t, lc.Stop(stopCtx), "Stop must remain idempotent after rollback")

	// goleak (deferred at top) asserts no sweeper goroutine survives.
}
