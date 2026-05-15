package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// noopStop is an idempotent stand-in for signal.NotifyContext's CancelFunc.
func noopStop() {}

// watchdogTestGrace is the (real, injected-clock) grace used by the
// watchdog branch test. Small so the timer fires quickly; the dispatch
// under test blocks until forceExit, so there is no completion race.
const watchdogTestGrace = testtime.D50ms

// largeTestGrace is used by the branches where the watchdog timer must NOT
// fire before the command returns — far longer than any in-test work.
const largeTestGrace = testtime.CtxLong

// TestRunWithSignal_NoSignal_CleanReturn pins the no-signal path: dispatch
// returns normally, the watchdog goroutine observes `done` (not ctx.Done)
// and exits without ever creating the grace timer or force-killing.
func TestRunWithSignal_NoSignal_CleanReturn(t *testing.T) {
	t.Parallel()
	ctx := context.Background() // never canceled
	var forced atomic.Int64

	code := runWithSignal(ctx, noopStop, clock.Real(), largeTestGrace, nil,
		func(context.Context, []string) int { return 0 },
		func(int) { forced.Add(1) })

	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if n := forced.Load(); n != 0 {
		t.Fatalf("forceExit called %d times with no signal; want 0", n)
	}
}

// TestRunWithSignal_CtxAware_GracefulNoForce pins the graceful path: on
// signal, a ctx-aware command observes the canceled ctx and returns before
// the (large) grace timer fires, so `done` closes first and forceExit is
// NOT called. runWithSignal returns the command's exit code.
func TestRunWithSignal_CtxAware_GracefulNoForce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate signal already delivered
	var forced atomic.Int64

	code := runWithSignal(ctx, noopStop, clock.Real(), largeTestGrace, nil,
		// ctx-aware: returns promptly because ctx is canceled.
		func(c context.Context, _ []string) int {
			<-c.Done()
			return ExitRuntime
		},
		func(int) { forced.Add(1) })

	if code != ExitRuntime {
		t.Fatalf("code = %d, want ExitRuntime(%d)", code, ExitRuntime)
	}
	if n := forced.Load(); n != 0 {
		t.Fatalf("forceExit called %d times on graceful ctx-aware path; want 0", n)
	}
}

// TestRunWithSignal_CtxIgnoring_WatchdogForces pins the regression fix
// (PR #502 review Point 2): on signal, a ctx-ignoring command does not
// return within grace, so the watchdog force-exits with 130. This is the
// deterministic stand-in for `gocell graph` swallowing Ctrl+C.
func TestRunWithSignal_CtxIgnoring_WatchdogForces(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // signal delivered

	forcedCode := make(chan int, 1)
	release := make(chan struct{})

	go func() {
		_ = runWithSignal(ctx, noopStop, clock.Real(), watchdogTestGrace, nil,
			// ctx-ignoring: blocks until the watchdog fires (mirrors a
			// non-ctx-native go/packages / worktree-sandbox command).
			func(context.Context, []string) int {
				<-release
				return 0
			},
			func(code int) {
				forcedCode <- code
				close(release) // let the fake command unwind so the goroutine ends
			})
	}()

	select {
	case code := <-forcedCode:
		if code != 130 {
			t.Fatalf("watchdog forceExit code = %d, want 130 (128+SIGINT)", code)
		}
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("watchdog did not force-exit a ctx-ignoring command in time")
	}
}
