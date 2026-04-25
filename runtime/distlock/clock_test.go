package distlock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

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

// TestRealClock_NewTimer_FiresAfterDuration verifies that a real timer fires.
// Uses 10ms — the minimum sane value for race-detector scheduling overhead.
// Wall-clock waiting is unavoidable for testing realClock timers.
func TestRealClock_NewTimer_FiresAfterDuration(t *testing.T) {
	clk := distlock.RealClockForTest()

	const d = 10 * time.Millisecond
	timer := clk.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C():
		// Good — timer fired.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("realClock timer did not fire within 500ms (expected ~10ms)")
	}
}

// TestRealClock_NewTimer_StopReturnsTrueWhenNotFired verifies Stop() returns
// true if called before the timer fires.
func TestRealClock_NewTimer_StopReturnsTrueWhenNotFired(t *testing.T) {
	clk := distlock.RealClockForTest()

	// Use a very large duration so the timer won't fire before we stop it.
	timer := clk.NewTimer(10 * time.Minute)
	stopped := timer.Stop()
	if !stopped {
		t.Error("Stop() should return true when timer has not yet fired")
	}
}

// TestRealClock_NewTimer_ResetFiresAfterReset verifies that Reset() re-arms a
// timer so it fires at the new deadline.
// Uses 10ms wall-clock wait — unavoidable for realClock timer testing.
func TestRealClock_NewTimer_ResetFiresAfterReset(t *testing.T) {
	clk := distlock.RealClockForTest()

	// Start a timer with a long duration, stop it, reset to 10ms.
	timer := clk.NewTimer(10 * time.Minute)
	timer.Stop()
	timer.Reset(10 * time.Millisecond)

	select {
	case <-timer.C():
		// Good — timer fired after reset.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timer did not fire after Reset(10ms) within 500ms")
	}
}

// TestRealClock_Since verifies that Since returns a non-negative duration.
func TestRealClock_Since(t *testing.T) {
	clk := distlock.RealClockForTest()

	past := time.Now().Add(-100 * time.Millisecond)
	d := clk.Since(past)
	if d < 0 {
		t.Errorf("Since(past) = %v, want >= 0", d)
	}
	if d > 10*time.Second {
		t.Errorf("Since(100ms ago) = %v, unexpectedly large", d)
	}
}
