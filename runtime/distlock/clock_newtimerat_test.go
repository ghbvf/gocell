package distlock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// TestRealClock_NewTimerAt_FiresAtDeadline verifies that the real clock's
// NewTimerAt creates a timer that fires when wall-clock time reaches the
// absolute deadline.
//
// Uses 10ms — minimum sane value for race-detector scheduling overhead.
func TestRealClock_NewTimerAt_FiresAtDeadline(t *testing.T) {
	clk := distlock.RealClockForTest()

	deadline := time.Now().Add(10 * time.Millisecond)
	timer := clk.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("realClock.NewTimerAt did not fire within 500ms (expected ~10ms)")
	}
}

// TestRealClock_NewTimerAt_PastDeadline_FiresImmediately verifies that
// passing a deadline in the past still produces a firing timer (negative
// duration is normalized to fire-immediately by time.NewTimer).
func TestRealClock_NewTimerAt_PastDeadline_FiresImmediately(t *testing.T) {
	clk := distlock.RealClockForTest()

	deadline := time.Now().Add(-1 * time.Second)
	timer := clk.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("realClock.NewTimerAt(past) did not fire within 500ms")
	}
}
