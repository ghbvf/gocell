package clock_test

import (
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
