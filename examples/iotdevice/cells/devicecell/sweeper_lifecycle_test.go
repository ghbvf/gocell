package devicecell

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// sweeperOnStartSettleDelay is the time given to the reconcile loop to either
// panic (regression) or settle before we cancel the context. Short enough to
// keep the test snappy; long enough that the recover()/done channel race is not
// flaky.
const sweeperOnStartSettleDelay = 50 * time.Millisecond

// sweeperOnStartReturnTimeout caps how long we wait for OnStart to return after
// ctx cancel. reconcile.Loop.Start is non-blocking — it spawns the worker pool,
// runs a fast startup probe, and returns; OnStart returns promptly regardless
// of the (real-clock, 30s) TickerTrigger cadence. 2s is generous belt-and-
// suspenders for slow CI.
const sweeperOnStartReturnTimeout = 2 * time.Second

// TestDeviceCell_CommandSweeperLoop_OnStartClean verifies the device-command
// reconcile.Loop is wired into the cell as a lifecycle hook whose OnStart is
// invoked safely (no panic) and whose OnStop is idempotent + nil-safe. This is
// the devicecell-level integration of the kernel Sweeper → reconcile.Reconciler
// migration: Init builds a lifecycle hook named "devicecommand.sweeper" backed
// by Loop.Start / Loop.Stop, and the worker exits cleanly on ctx cancel.
//
// The historical regression this guards (PR 441 F2): a Sweeper constructed
// without a clock would panic at clock.MustHaveClock inside the lifecycle
// goroutine. The Sweeper now holds a mandatory business clock (NewSweeper) and
// the Loop owns the control-plane scheduling, so Start settles cleanly.
func TestDeviceCell_CommandSweeperLoop_OnStartClean(t *testing.T) {
	defer goleak.VerifyNone(t)
	c := newTestCell()
	rec := newTestRec()
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	hook := lifecycleHookByName(t, snap.LifecycleHooks, "devicecommand.sweeper")
	require.NotNil(t, hook.OnStart)
	require.NotNil(t, hook.OnStop)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("OnStart panicked: %v", r)
			}
		}()
		done <- hook.OnStart(ctx)
	}()

	// Give the sweeper a moment to either panic or settle into the select loop.
	time.Sleep(sweeperOnStartSettleDelay) //archtest:allow:test-sleep give panic-vs-settle race a deterministic window
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "OnStart must not panic and must return nil after ctx cancel")
	case <-time.After(sweeperOnStartReturnTimeout):
		t.Fatal("OnStart did not return within budget after ctx cancel")
	}

	require.NoError(t, hook.OnStop(context.Background()), "OnStop must be idempotent and nil-safe")
}
