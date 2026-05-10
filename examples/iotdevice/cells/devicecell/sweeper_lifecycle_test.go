package devicecell

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDeviceCell_SweeperLifecycle_OnStartDoesNotPanic verifies the command
// sweeper's OnStart hook is invoked safely. PR 441 review F2 root cause:
// cell.go::initSlices constructs &kcommand.Sweeper{...} without setting Clk,
// so kernel/command/sweeper.go::Start hits clock.MustHaveClock(nil, ...) and
// panics inside the lifecycle goroutine.
//
// Pre-Wave-2/Wave-3 fix: this test fails (recovered panic). After Wave 2
// (Sweeper factory + private fields) and Wave 3 (cell.go switches to
// kcommand.NewSweeper), Sweeper.Start enters the select loop without panic
// and exits cleanly when ctx is canceled.
func TestDeviceCell_SweeperLifecycle_OnStartDoesNotPanic(t *testing.T) {
	c := newTestCell()
	rec := newTestRec()
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()
	require.Len(t, snap.LifecycleHooks, 1, "expect one lifecycle hook (sweeper)")
	hook := snap.LifecycleHooks[0]
	require.Equal(t, "devicecommand.sweeper", hook.Name)
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
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "OnStart must not panic and must return nil after ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("OnStart did not return within 2s of ctx cancel")
	}

	require.NoError(t, hook.OnStop(context.Background()), "OnStop must be idempotent and nil-safe")
}
