package distlock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/distlock"
)

const clockDNeg100ms = -100 * time.Millisecond

// TestRealClock_NowReturnsCurrentTime verifies that the real clock's Now()
// returns a time within 1ms of time.Now().
func TestRealClock_NowReturnsCurrentTime(t *testing.T) {
	clk := distlock.RealClockForTest()
	before := time.Now()
	got := clk.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("realClock.Now() = %v, not within window [%v, %v]", got, before, after)
	}
}

// TestRealClock_NewTimerAt_FiresAfterDuration verifies that a real timer fires.
// Uses 10ms — the minimum sane value for race-detector scheduling overhead.
// Wall-clock waiting is unavoidable for testing realClock timers.
func TestRealClock_NewTimerAt_FiresAfterDuration(t *testing.T) {
	clk := distlock.RealClockForTest()

	timer := clk.NewTimerAt(clk.Now().Add(testtime.D10ms))
	defer timer.Stop()

	select {
	case <-timer.C():
		// Good — timer fired.
	case <-time.After(testtime.D500ms):
		t.Fatal("realClock timer did not fire within 500ms (expected ~10ms)")
	}
}

// TestRealClock_NewTimerAt_StopReturnsTrueWhenNotFired verifies Stop() returns
// true if called before the timer fires.
func TestRealClock_NewTimerAt_StopReturnsTrueWhenNotFired(t *testing.T) {
	clk := distlock.RealClockForTest()

	// Deadline far in the future so the timer won't fire before we stop it.
	timer := clk.NewTimerAt(clk.Now().Add(testtime.D10min))
	stopped := timer.Stop()
	if !stopped {
		t.Error("Stop() should return true when timer has not yet fired")
	}
}

// TestRealClock_NewTimerAt_ResetFiresAfterReset verifies that Reset() re-arms
// a timer so it fires at the new deadline.
// Uses 10ms wall-clock wait — unavoidable for realClock timer testing.
func TestRealClock_NewTimerAt_ResetFiresAfterReset(t *testing.T) {
	clk := distlock.RealClockForTest()

	// Start a timer with a far-future deadline, stop it, reset to 10ms.
	timer := clk.NewTimerAt(clk.Now().Add(testtime.D10min))
	timer.Stop()
	timer.Reset(testtime.D10ms)

	select {
	case <-timer.C():
		// Good — timer fired after reset.
	case <-time.After(testtime.D500ms):
		t.Fatal("timer did not fire after Reset(10ms) within 500ms")
	}
}

// TestRealClock_Since verifies that Since returns a non-negative duration.
func TestRealClock_Since(t *testing.T) {
	clk := distlock.RealClockForTest()

	past := time.Now().Add(clockDNeg100ms)
	d := clk.Since(past)
	if d < 0 {
		t.Errorf("Since(past) = %v, want >= 0", d)
	}
	if d > testtime.D10s {
		t.Errorf("Since(100ms ago) = %v, unexpectedly large", d)
	}
}
