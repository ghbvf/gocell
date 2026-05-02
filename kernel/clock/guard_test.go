package clock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestMustHaveClock_NilInterface panics when a plain nil interface is passed.
func TestMustHaveClock_NilInterface(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustHaveClock(nil, ...) did not panic")
		}
	}()
	clock.MustHaveClock(nil, "test.NilInterface")
}

// TestMustHaveClock_TypedNil panics when a typed-nil pointer is wrapped in the interface.
func TestMustHaveClock_TypedNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustHaveClock(typed-nil, ...) did not panic")
		}
	}()
	var c *clockmock.FakeClock // typed nil — non-nil interface value wrapping nil pointer
	clock.MustHaveClock(c, "test.TypedNil")
}

// TestMustHaveClock_ValidClock does not panic for a properly initialized clock.
func TestMustHaveClock_ValidClock(t *testing.T) {
	t.Parallel()
	c := clockmock.New(time.Time{})
	// Must not panic.
	clock.MustHaveClock(c, "test.ValidClock")
}

// TestMustHavePositiveInterval_Negative panics for a negative duration.
func TestMustHavePositiveInterval_Negative(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustHavePositiveInterval(-1s, ...) did not panic")
		}
	}()
	clock.MustHavePositiveInterval(-time.Second, "test.Negative")
}

// TestMustHavePositiveInterval_Zero panics for a zero duration.
func TestMustHavePositiveInterval_Zero(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustHavePositiveInterval(0, ...) did not panic")
		}
	}()
	clock.MustHavePositiveInterval(0, "test.Zero")
}

// TestMustHavePositiveInterval_Positive does not panic for a positive duration.
func TestMustHavePositiveInterval_Positive(t *testing.T) {
	t.Parallel()
	// Must not panic.
	clock.MustHavePositiveInterval(time.Second, "test.Positive")
}

// TestRealTimerResetAt_FiresAtAbsoluteDeadline verifies that realTimer.ResetAt
// fires the timer at the given absolute deadline.
func TestRealTimerResetAt_FiresAtAbsoluteDeadline(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	// Create with a far future deadline, then ResetAt to a near deadline.
	timer := c.NewTimerAt(time.Now().Add(testtime.D10s))
	timer.Stop()

	deadline := time.Now().Add(testtime.D30ms)
	timer.ResetAt(deadline)
	defer timer.Stop()

	select {
	case got := <-timer.C():
		if got.Before(deadline.Add(-testtime.D30ms)) {
			t.Errorf("timer fired too early: got %v, deadline %v", got, deadline)
		}
	case <-time.After(testtime.D200ms):
		t.Fatalf("timer did not fire within %v after ResetAt", testtime.D200ms)
	}
}

// TestRealTimerResetAt_PastDeadlineFiresImmediately verifies that ResetAt with
// a deadline in the past causes the timer to fire immediately.
func TestRealTimerResetAt_PastDeadlineFiresImmediately(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	timer := c.NewTimerAt(time.Now().Add(testtime.D10s))
	timer.Stop()
	timer.ResetAt(time.Now().Add(-time.Second)) // past deadline

	select {
	case <-timer.C():
		// Good.
	case <-time.After(testtime.D100ms):
		t.Fatalf("ResetAt(past deadline) did not fire within %v", testtime.D100ms)
	}
}

// TestRealTickerReset_DeliversAfterNewInterval verifies that realTicker.Reset
// causes the next tick to arrive within the new (shorter) interval.
func TestRealTickerReset_DeliversAfterNewInterval(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	tk := c.NewTicker(testtime.D10s)
	defer tk.Stop()

	// Reset to a short interval — tick should arrive within waitBudget.
	tk.Reset(testtime.D30ms)

	select {
	case <-tk.C():
		// Good — fired after reset to testtime.D30ms.
	case <-time.After(testtime.D200ms):
		t.Fatalf("ticker did not fire within %v after Reset(%v)", testtime.D200ms, testtime.D30ms)
	}
}
