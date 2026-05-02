package clock_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

const (
	realToleranceSlack     = 50 * time.Millisecond
	realTimerFireDelay     = 20 * time.Millisecond
	realTimerStopDelay     = time.Second
	realTimerSinceWindow   = 100 * time.Millisecond
	realTimerWaitFire      = 200 * time.Millisecond
	realTimerWaitStop      = 20 * time.Millisecond
	realTimerWaitImmediate = 50 * time.Millisecond
	realPastDeadlineDelta  = -100 * time.Millisecond
)

func TestRealClockTracksWallClock(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	t0 := time.Now()
	got := c.Now()
	t1 := time.Now()

	if got.Before(t0) || got.After(t1) {
		t.Fatalf("Real().Now() = %v, expected within [%v, %v]", got, t0, t1)
	}
}

func TestRealSinceMatchesStdlib(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	start := time.Now().Add(-realTimerSinceWindow)

	got := c.Since(start)

	if got < realTimerSinceWindow || got > realTimerSinceWindow+realToleranceSlack {
		t.Fatalf("Real().Since(start) = %v, expected ~%v", got, realTimerSinceWindow)
	}
}

func TestRealUntilMatchesStdlib(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	deadline := time.Now().Add(realTimerSinceWindow)

	got := c.Until(deadline)

	if got <= 0 || got > realTimerSinceWindow {
		t.Fatalf("Real().Until(deadline) = %v, expected (0, %v]", got, realTimerSinceWindow)
	}
}

func TestRealNewTimerAtFiresAfterDeadline(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	deadline := time.Now().Add(realTimerFireDelay)
	timer := c.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
		// expected
	case <-time.After(realTimerWaitFire):
		t.Fatalf("timer did not fire within %v", realTimerWaitFire)
	}
}

func TestRealNewTimerAtPastDeadlineFiresImmediately(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	deadline := time.Now().Add(realPastDeadlineDelta)
	timer := c.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
		// expected
	case <-time.After(realTimerWaitImmediate):
		t.Fatalf("past-deadline timer did not fire within %v", realTimerWaitImmediate)
	}
}

func TestRealTimerStopBeforeFire(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	timer := c.NewTimerAt(time.Now().Add(realTimerStopDelay))

	if !timer.Stop() {
		t.Fatal("Stop() returned false for active timer")
	}

	select {
	case <-timer.C():
		t.Fatal("timer fired after Stop()")
	case <-time.After(realTimerWaitStop):
	}
}

func TestRealTimerReset(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	timer := c.NewTimerAt(time.Now().Add(realTimerStopDelay))

	if !timer.Stop() {
		t.Fatal("Stop() returned false for active timer")
	}
	timer.Reset(realTimerFireDelay)

	select {
	case <-timer.C():
		// expected
	case <-time.After(realTimerWaitFire):
		t.Fatalf("timer did not fire within %v after Reset", realTimerWaitFire)
	}
}

func TestRealNewTickerDelivers(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	tk := c.NewTicker(realTimerFireDelay)
	defer tk.Stop()

	select {
	case <-tk.C():
	case <-time.After(realTimerWaitFire):
		t.Fatalf("ticker did not fire within %v", realTimerWaitFire)
	}
}

func TestRealAfterFuncRuns(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	var ran atomic.Int32
	done := make(chan struct{})
	tm := c.AfterFunc(time.Now().Add(realTimerFireDelay), func() {
		ran.Add(1)
		close(done)
	})
	defer tm.Stop()

	select {
	case <-done:
	case <-time.After(realTimerWaitFire):
		t.Fatalf("AfterFunc callback did not run within %v", realTimerWaitFire)
	}
	if got := ran.Load(); got != 1 {
		t.Fatalf("AfterFunc ran %d times, want 1", got)
	}
}

func TestRealAfterFuncStopPrevents(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	var ran atomic.Int32
	tm := c.AfterFunc(time.Now().Add(realTimerStopDelay), func() { ran.Add(1) })
	if !tm.Stop() {
		t.Fatal("Stop() returned false for active AfterFunc timer")
	}

	time.Sleep(realTimerWaitStop) //archtest:allow:test-sleep wait long enough to detect a missed Stop on the real clock
	if got := ran.Load(); got != 0 {
		t.Fatalf("stopped AfterFunc ran %d times, want 0", got)
	}
}

func TestRealSleepReturnsAfterDeadline(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	start := time.Now()
	if err := c.Sleep(context.Background(), start.Add(realTimerFireDelay)); err != nil {
		t.Fatalf("Sleep returned %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < realTimerFireDelay {
		t.Fatalf("Sleep returned after %v, want at least %v", elapsed, realTimerFireDelay)
	}
}

func TestRealSleepCtxCancel(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- c.Sleep(ctx, time.Now().Add(realTimerStopDelay))
	}()

	time.Sleep(realTimerWaitStop) //archtest:allow:test-sleep give Sleep goroutine time to install its timer before ctx cancel
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Sleep returned nil after ctx cancel; want non-nil error")
		}
	case <-time.After(realTimerWaitFire):
		t.Fatal("Sleep did not return after ctx cancel")
	}
}

func TestRealSleepPastDeadline(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	if err := c.Sleep(context.Background(), time.Now().Add(realPastDeadlineDelta)); err != nil {
		t.Fatalf("Sleep on past deadline returned %v, want nil", err)
	}
}
