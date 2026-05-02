package clockmock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestFakeClock_Advance_ZeroDoesNotFireAnyTimerOrTicker verifies that
// Advance(0) does not trigger any timer or ticker and leaves Now() unchanged.
func TestFakeClock_Advance_ZeroDoesNotFireAnyTimerOrTicker(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(epoch)
	before := fc.Now()

	timer := fc.NewTimerAt(epoch.Add(testtime.D1s))
	defer timer.Stop()

	tk := fc.NewTicker(testtime.D1s)
	defer tk.Stop()

	fc.Advance(0)

	if got := fc.Now(); !got.Equal(before) {
		t.Errorf("Now() = %v after Advance(0), want %v (unchanged)", got, before)
	}

	select {
	case <-timer.C():
		t.Error("timer fired after Advance(0)")
	default:
	}
	select {
	case <-tk.C():
		t.Error("ticker fired after Advance(0)")
	default:
	}
}

// TestFakeClock_AfterFunc_MixedWithTimers_SameDeadline verifies that when an
// AfterFunc timer and a plain timer share the same deadline, both fire after a
// single Advance that reaches that deadline.
func TestFakeClock_AfterFunc_MixedWithTimers_SameDeadline(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(epoch)
	deadline := epoch.Add(testtime.D3s)

	// Plain timer at the same deadline.
	plain := fc.NewTimerAt(deadline)
	defer plain.Stop()

	// AfterFunc timer at the same deadline.
	callbackFired := make(chan struct{}, 1)
	af := fc.AfterFunc(deadline, func() {
		callbackFired <- struct{}{}
	})
	defer af.Stop()

	fc.Advance(testtime.D3s)

	// Plain timer channel must deliver.
	select {
	case <-plain.C():
	case <-time.After(testtime.D100ms):
		t.Error("plain timer at T+3s did not fire after Advance(3s)")
	}

	// AfterFunc callback must run.
	select {
	case <-callbackFired:
	case <-time.After(testtime.D100ms):
		t.Error("AfterFunc callback at T+3s did not run after Advance(3s)")
	}
}

// TestFakeTimer_ResetAt_FutureDeadline verifies that ResetAt arms the timer at
// the specified absolute deadline and fires when Advance reaches it.
func TestFakeTimer_ResetAt_FutureDeadline(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(epoch)
	timer := fc.NewTimerAt(epoch.Add(testtime.D10s))
	timer.Stop()

	// Re-arm at a nearer absolute deadline. wasActive is false (we stopped it)
	// — accept the return without branching to keep staticcheck SA9003 happy.
	target := epoch.Add(testtime.D3s)
	_ = timer.ResetAt(target)

	// Before deadline — should not fire.
	fc.Advance(testtime.D2s)
	select {
	case <-timer.C():
		t.Error("timer fired before ResetAt deadline")
	default:
	}

	// At deadline — should fire.
	fc.Advance(testtime.D1s) // fc.now = epoch+3s == target
	select {
	case <-timer.C():
	case <-time.After(testtime.D100ms):
		t.Error("timer did not fire after Advance reached ResetAt deadline")
	}
}

// TestFakeTimer_ResetAt_PastDeadline_FiresImmediately verifies that ResetAt
// with a deadline <= fc.now delivers immediately.
func TestFakeTimer_ResetAt_PastDeadline_FiresImmediately(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(epoch)
	fc.Advance(testtime.D5s) // fc.now = epoch+5s

	timer := fc.NewTimerAt(epoch.Add(testtime.D10s))
	timer.Stop()

	// ResetAt to a time that is in the past relative to fc.now.
	timer.ResetAt(epoch.Add(testtime.D3s))

	select {
	case <-timer.C():
	case <-time.After(testtime.D100ms):
		t.Error("ResetAt(past deadline) did not fire immediately")
	}
}

// TestFakeClock_NewTimerAt_PastDeadline_ImmediateReceive verifies that a timer
// created with a deadline that is already in the past has a value ready on its
// channel immediately (no Advance required).
func TestFakeClock_NewTimerAt_PastDeadline_ImmediateReceive(t *testing.T) {
	t.Parallel()

	fc := clockmock.New(epoch)
	// Advance to T+10s so that epoch is a "past" deadline.
	fc.Advance(testtime.D10s)

	past := epoch // strictly before fc.now
	timer := fc.NewTimerAt(past)
	defer timer.Stop()

	// Channel must already have a value — no Advance needed.
	select {
	case got := <-timer.C():
		_ = got // just verify receive is non-blocking
	default:
		t.Error("NewTimerAt(past deadline) channel was empty; expected immediate delivery")
	}
}
