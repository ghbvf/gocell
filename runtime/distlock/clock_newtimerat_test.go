package distlock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestRealClock_NewTimerAt_FiresAtDeadline verifies that the real clock's
// NewTimerAt creates a timer that fires when wall-clock time reaches the
// absolute deadline.
//
// Uses 10ms — minimum sane value for race-detector scheduling overhead.
func TestRealClock_NewTimerAt_FiresAtDeadline(t *testing.T) {
	clk := clock.Real()

	deadline := time.Now().Add(testtime.D10ms)
	timer := clk.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
	case <-time.After(testtime.D500ms):
		t.Fatal("realClock.NewTimerAt did not fire within 500ms (expected ~10ms)")
	}
}

// TestRealClock_NewTimerAt_PastDeadline_FiresImmediately verifies that
// passing a deadline in the past still produces a firing timer (negative
// duration is normalized to fire-immediately by time.NewTimer).
func TestRealClock_NewTimerAt_PastDeadline_FiresImmediately(t *testing.T) {
	clk := clock.Real()

	deadline := time.Now().Add(testtime.DNeg1s)
	timer := clk.NewTimerAt(deadline)
	defer timer.Stop()

	select {
	case <-timer.C():
	case <-time.After(testtime.D500ms):
		t.Fatal("realClock.NewTimerAt(past) did not fire within 500ms")
	}
}
