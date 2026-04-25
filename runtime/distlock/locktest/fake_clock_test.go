package locktest_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// TestFakeClock_Now_ReturnsConfiguredTime verifies initial Now() value.
func TestFakeClock_Now_ReturnsConfiguredTime(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	if got := fc.Now(); !got.Equal(epoch) {
		t.Errorf("Now() = %v, want %v", got, epoch)
	}
}

// TestFakeClock_Advance_FiresDueTimers verifies that multiple timers at
// different deadlines fire when Advance moves past their deadline.
func TestFakeClock_Advance_FiresDueTimers(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)

	// Timer at +1s and +2s.
	t1 := fc.NewTimer(1 * time.Second)
	t2 := fc.NewTimer(2 * time.Second)

	// Advance 1.5s — only t1 should fire.
	fc.Advance(1500 * time.Millisecond)

	select {
	case <-t1.C():
	default:
		t.Error("t1 (deadline +1s) should have fired after Advance(1.5s)")
	}
	select {
	case <-t2.C():
		t.Error("t2 (deadline +2s) should NOT have fired after Advance(1.5s)")
	default:
	}

	// Advance another 1s — t2 should now fire.
	fc.Advance(1 * time.Second)
	select {
	case <-t2.C():
	default:
		t.Error("t2 (deadline +2s) should have fired after total Advance(2.5s)")
	}
}

// TestFakeClock_Advance_DoesNotFireFutureTimers verifies that timers with
// future deadlines do not fire prematurely.
func TestFakeClock_Advance_DoesNotFireFutureTimers(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(10 * time.Second)

	// Advance less than the timer deadline.
	fc.Advance(5 * time.Second)

	select {
	case <-timer.C():
		t.Error("timer should not have fired; deadline not yet reached")
	default:
	}

	// Clean up.
	timer.Stop()
}

// TestFakeClock_Advance_PreservesOrder verifies that timers fire in deadline
// order (earliest first) when multiple timers have different deadlines.
func TestFakeClock_Advance_PreservesOrder(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)

	// Create timers in reverse order of deadlines.
	t3 := fc.NewTimer(3 * time.Second)
	t1 := fc.NewTimer(1 * time.Second)
	t2 := fc.NewTimer(2 * time.Second)

	// Advance past all deadlines in one step.
	fired := make([]int, 0, 3)
	fc.Advance(4 * time.Second)

	// All three should have fired. Drain channels.
	for _, pair := range []struct {
		timer distlockTimer
		idx   int
	}{
		{t1, 1}, {t2, 2}, {t3, 3},
	} {
		select {
		case <-pair.timer.C():
			fired = append(fired, pair.idx)
		default:
			t.Errorf("timer t%d did not fire after Advance(4s)", pair.idx)
		}
	}
	if len(fired) != 3 {
		t.Errorf("expected 3 timers fired, got %d", len(fired))
	}
}

// distlockTimer is a local alias for the Timer interface returned by FakeClock.
// Used in TestFakeClock_Advance_PreservesOrder to hold heterogeneous timers.
type distlockTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// TestFakeClock_Timer_Stop_BeforeFire verifies that Stop() returns true and
// prevents the timer from firing.
func TestFakeClock_Timer_Stop_BeforeFire(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(5 * time.Second)

	if !timer.Stop() {
		t.Error("Stop() before fire should return true")
	}

	// Advance past deadline — timer should not fire (was stopped).
	fc.Advance(10 * time.Second)

	select {
	case <-timer.C():
		t.Error("stopped timer should not fire")
	default:
	}

	// PendingTimers should be 0 since the timer was stopped.
	if n := fc.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers = %d, want 0 after stop", n)
	}
}

// TestFakeClock_Timer_Stop_AfterFire verifies that Stop() returns false after
// the timer has already fired.
func TestFakeClock_Timer_Stop_AfterFire(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(1 * time.Second)

	// Fire the timer by advancing.
	fc.Advance(2 * time.Second)

	// Drain the channel.
	select {
	case <-timer.C():
	default:
		t.Fatal("timer should have fired")
	}

	// Stop after fire should return false.
	if timer.Stop() {
		t.Error("Stop() after fire should return false")
	}
}

// TestFakeClock_Timer_Stop_TwiceIdempotent verifies that calling Stop() twice
// is safe (second call returns false).
func TestFakeClock_Timer_Stop_TwiceIdempotent(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(5 * time.Second)

	if !timer.Stop() {
		t.Error("first Stop() should return true")
	}
	if timer.Stop() {
		t.Error("second Stop() should return false")
	}
}

// TestFakeClock_Timer_Reset_ToFutureDeadline verifies Reset() re-arms the
// timer to a future deadline.
func TestFakeClock_Timer_Reset_ToFutureDeadline(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(1 * time.Second)

	// Stop and reset to 5s.
	timer.Stop()
	timer.Reset(5 * time.Second)

	// Advance 3s — timer should not fire yet.
	fc.Advance(3 * time.Second)
	select {
	case <-timer.C():
		t.Error("timer should not fire at 3s after Reset(5s)")
	default:
	}

	// Advance 3 more seconds — now total is 6s, past deadline.
	fc.Advance(3 * time.Second)
	select {
	case <-timer.C():
		// Good — fired.
	default:
		t.Error("timer should have fired at 6s after Reset(5s)")
	}
}

// TestFakeClock_Timer_Reset_ToPastDeadline_FiresOnNextAdvance verifies that
// Reset(<=0) marks the timer as fired and sends on the channel.
func TestFakeClock_Timer_Reset_ToPastDeadline_FiresOnNextAdvance(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(10 * time.Second)

	// Reset to 0 — should fire immediately.
	timer.Reset(0)

	select {
	case <-timer.C():
		// Good — fired immediately on Reset(0).
	default:
		t.Error("timer with Reset(0) should fire immediately")
	}
}

// TestFakeClock_Since verifies that Since uses the configured Now.
func TestFakeClock_Since(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)

	// Advance to T+5s.
	fc.Advance(5 * time.Second)

	// Since(epoch) should be 5s.
	d := fc.Since(epoch)
	if d != 5*time.Second {
		t.Errorf("Since(epoch) = %v, want 5s", d)
	}
}

// TestFakeClock_NewTimer_ZeroDuration verifies that NewTimer(0) fires immediately
// (the channel already has a value, no Advance needed).
func TestFakeClock_NewTimer_ZeroDuration(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(0)

	select {
	case <-timer.C():
		// Good — fired immediately.
	default:
		t.Error("NewTimer(0) should fire immediately")
	}
	// PendingTimers should be 0 (fired timers are not counted).
	if n := fc.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers = %d after immediate timer, want 0", n)
	}
}

// TestFakeClock_NewTimer_NegativeDuration verifies that NewTimer(<0) fires immediately.
func TestFakeClock_NewTimer_NegativeDuration(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	timer := fc.NewTimer(-1 * time.Second)

	select {
	case <-timer.C():
		// Good — fired immediately.
	default:
		t.Error("NewTimer(-1s) should fire immediately")
	}
}

// TestFakeClock_PendingTimers_CountsOnlyArmed verifies that PendingTimers()
// counts only non-stopped, non-fired timers.
func TestFakeClock_PendingTimers_CountsOnlyArmed(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)

	if fc.PendingTimers() != 0 {
		t.Errorf("initial PendingTimers = %d, want 0", fc.PendingTimers())
	}

	t1 := fc.NewTimer(1 * time.Second)
	t2 := fc.NewTimer(2 * time.Second)
	t3 := fc.NewTimer(3 * time.Second)

	if fc.PendingTimers() != 3 {
		t.Errorf("PendingTimers = %d, want 3 after creating 3 timers", fc.PendingTimers())
	}

	// Stop t2.
	t2.Stop()
	if fc.PendingTimers() != 2 {
		t.Errorf("PendingTimers = %d, want 2 after stopping one", fc.PendingTimers())
	}

	// Advance past t1.
	fc.Advance(1500 * time.Millisecond)
	<-t1.C()
	if fc.PendingTimers() != 1 {
		t.Errorf("PendingTimers = %d, want 1 after t1 fired", fc.PendingTimers())
	}

	// Stop the remaining timer.
	t3.Stop()
	if fc.PendingTimers() != 0 {
		t.Errorf("PendingTimers = %d, want 0 after all stopped or fired", fc.PendingTimers())
	}
}
