package locktest_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestFakeClock_NewTimerAt_FiresAtDeadline verifies that a timer created with
// NewTimerAt fires the moment fc.now >= deadline.
func TestFakeClock_NewTimerAt_FiresAtDeadline(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	deadline := epoch.Add(5 * time.Second)

	timer := fc.NewTimerAt(deadline)

	// fc.now=T+4s — not yet at deadline.
	fc.Advance(4 * time.Second)
	select {
	case <-timer.C():
		t.Error("timer at T+5s should not fire at fc.now=T+4s")
	default:
	}

	// fc.now=T+5s — should fire.
	fc.Advance(1 * time.Second)
	select {
	case <-timer.C():
	case <-time.After(100 * time.Millisecond):
		t.Error("timer at T+5s should fire when fc.now reaches T+5s")
	}
}

// TestFakeClock_NewTimerAt_NotRebaselinedAcrossIntermediateAdvance is the
// regression test for the TC-3 flaky race. It pins the contract that
// NewTimerAt(absoluteDeadline) does NOT re-baseline against fc.now if Advance
// is called between deadline capture and timer creation.
//
// Sequence:
//  1. fc.now = T+0
//  2. Code captures deadline = T+5s (e.g. from heap.Peek().nextRenew).
//  3. ⚠️ Advance(3s) interleaves before timer creation — fc.now = T+3s.
//  4. Code calls NewTimerAt(T+5s).
//  5. Advance(2s) — fc.now = T+5s. Timer MUST fire.
//
// With the old NewTimer(d) API, step 4 would compute d=5s-0s=5s captured at
// step 2, then NewTimer(5s) at step 4 with fc.now=T+3s would arm at T+8s,
// which step 5 cannot reach — exact root-cause of TC-3 flake on GitHub
// runner under -race.
func TestFakeClock_NewTimerAt_NotRebaselinedAcrossIntermediateAdvance(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	deadline := epoch.Add(5 * time.Second)

	// Step 3: Advance happens BEFORE timer creation.
	fc.Advance(3 * time.Second) // fc.now = T+3s

	// Step 4: NewTimerAt uses ABSOLUTE deadline; immune to intermediate Advance.
	timer := fc.NewTimerAt(deadline)

	// Step 5: Reach the absolute deadline — must fire.
	fc.Advance(2 * time.Second) // fc.now = T+5s
	select {
	case <-timer.C():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("NewTimerAt(T+5s) must fire at fc.now=T+5s regardless of " +
			"intermediate Advance(3s) — pre-fix TC-3 race regression")
	}
}

// TestFakeClock_NewTimerAt_PastDeadlineFiresImmediately verifies that a
// deadline already in the past fires right away (mirrors NewTimer(d<=0)).
func TestFakeClock_NewTimerAt_PastDeadlineFiresImmediately(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)
	fc.Advance(10 * time.Second) // fc.now = T+10s

	// Deadline at T+5s is in the past relative to fc.now.
	timer := fc.NewTimerAt(epoch.Add(5 * time.Second))

	select {
	case <-timer.C():
	case <-time.After(100 * time.Millisecond):
		t.Error("NewTimerAt(past-deadline) must fire immediately")
	}

	if n := fc.PendingTimers(); n != 0 {
		t.Errorf("PendingTimers = %d after immediate-fire timer, want 0", n)
	}
}

// TestFakeClock_NewTimerAt_DeadlineEqualsNow_FiresImmediately covers the
// boundary where deadline == fc.now (zero-duration equivalent).
func TestFakeClock_NewTimerAt_DeadlineEqualsNow_FiresImmediately(t *testing.T) {
	fc := locktest.NewFakeClock(epoch)

	timer := fc.NewTimerAt(epoch) // deadline == fc.now

	select {
	case <-timer.C():
	case <-time.After(100 * time.Millisecond):
		t.Error("NewTimerAt(now) must fire immediately")
	}
}

// TestFakeClock_NewTimerAt_ConcurrentWithAdvance stress-tests the atomicity
// guarantee under concurrent Advance + NewTimerAt across many iterations.
// Each iteration: a goroutine calls NewTimerAt(deadline) while the main
// goroutine calls Advance(d). Whichever ordering wins, the timer's fire
// status must reflect "deadline reached" (fc.now >= deadline).
//
// This guard prevents future regressions if NewTimerAt becomes non-atomic.
func TestFakeClock_NewTimerAt_ConcurrentWithAdvance(t *testing.T) {
	const iterations = 200
	for i := range iterations {
		fc := locktest.NewFakeClock(epoch)
		deadline := epoch.Add(5 * time.Second)

		var wg sync.WaitGroup
		var timer interface {
			C() <-chan time.Time
			Stop() bool
		}

		wg.Go(func() {
			timer = fc.NewTimerAt(deadline)
		})

		// Advance enough to reach the deadline. Whether NewTimerAt happens
		// before or after Advance, the timer must fire because fc.now ends at
		// T+5s == deadline.
		fc.Advance(5 * time.Second)
		wg.Wait()

		select {
		case <-timer.C():
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("iter %d: timer at T+5s did not fire after Advance(5s) "+
				"(concurrent NewTimerAt/Advance ordering broke atomicity)", i)
		}
	}
}
