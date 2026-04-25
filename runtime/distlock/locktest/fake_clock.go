package locktest

import (
	"sync"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// Compile-time assertions.
var (
	_ distlock.Clock = (*FakeClock)(nil)
	_ distlock.Timer = (*FakeTimer)(nil)
)

// FakeClock is a controllable Clock implementation for deterministic testing.
// Time advances only when Advance is called. All timers created by this clock
// fire synchronously when Advance moves past their deadline.
//
// All methods are safe for concurrent use.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*FakeTimer
}

// NewFakeClock creates a FakeClock starting at the given initial time.
// If zero, defaults to a fixed epoch for determinism.
func NewFakeClock(initial time.Time) *FakeClock {
	if initial.IsZero() {
		initial = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: initial}
}

// Now implements distlock.Clock.
func (fc *FakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

// Since implements distlock.Clock.
func (fc *FakeClock) Since(t time.Time) time.Duration {
	return fc.Now().Sub(t)
}

// NewTimerAt implements distlock.Clock.
// The returned FakeTimer fires when Advance moves fc.now to or past deadline.
//
// The deadline write and timer registration happen under a single fc.mu hold,
// which is what makes the API race-free against concurrent Advance calls — a
// duration-based API would require two non-atomic clock interactions. See
// clock.go Clock.NewTimerAt for the design rationale.
func (fc *FakeClock) NewTimerAt(deadline time.Time) distlock.Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &FakeTimer{
		deadline: deadline,
		ch:       make(chan time.Time, 1),
		clock:    fc,
	}
	// Deadline already reached → fire immediately, do not enqueue.
	if !fc.now.Before(deadline) {
		ft.ch <- fc.now
		ft.fired = true
	} else {
		fc.timers = append(fc.timers, ft)
	}
	return ft
}

// Advance moves the clock forward by d and fires all timers whose deadline
// has passed. Timers are fired in deadline order (earliest first).
// Returns after all due timers' channels have been written.
func (fc *FakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	now := fc.now

	// Collect due timers and mark them fired atomically under fc.mu.
	// Marking fired here (under fc.mu) prevents a data race with Stop(), which
	// reads ft.fired under fc.mu. Due timers are removed from fc.timers so
	// Stop() will not find them in the list, but it may still read ft.fired
	// when iterating; the write must therefore also be under fc.mu.
	var due []*FakeTimer
	var remaining []*FakeTimer
	for _, t := range fc.timers {
		if t.stopped {
			continue
		}
		if !t.fired && !now.Before(t.deadline) {
			t.fired = true // mark under fc.mu to avoid race with Stop
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	fc.timers = remaining
	fc.mu.Unlock()

	// Send to channels outside the lock to avoid deadlock when timer receivers
	// call back into the clock (e.g. FakeClock.Now()). fired is already true so
	// no guard check is needed here.
	for _, t := range due {
		// Non-blocking send — channel is buffered (cap 1).
		select {
		case t.ch <- now:
		default:
		}
	}
}

// PendingTimers returns the number of active (non-stopped, non-fired) timers
// currently registered with this clock. Useful in tests to wait until the
// manager goroutine has re-registered a timer after a renew.
func (fc *FakeClock) PendingTimers() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	count := 0
	for _, t := range fc.timers {
		if !t.stopped && !t.fired {
			count++
		}
	}
	return count
}

// FakeTimer is a single-fire timer controlled by FakeClock.
//
// All mutable fields (stopped, fired, deadline) are protected by the parent
// FakeClock's mu, not by a per-timer lock. This is necessary because
// PendingTimers(), Advance(), Stop(), and Reset() all need to read/write these
// fields atomically relative to the clock's timer list.
type FakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	clock    *FakeClock
	stopped  bool
	fired    bool
}

// C implements distlock.Timer.
func (ft *FakeTimer) C() <-chan time.Time { return ft.ch }

// Stop implements distlock.Timer.
// Returns true if the timer was stopped before firing; false if it had already
// fired or been stopped.
//
// Both stopped and fired are read by PendingTimers/Advance under fc.mu, so
// Stop must also hold fc.mu when modifying stopped to avoid a data race.
// The removal from fc.timers and the stopped flag update are done atomically
// under fc.mu so Advance cannot observe a partially-stopped timer.
func (ft *FakeTimer) Stop() bool {
	fc := ft.clock
	fc.mu.Lock()
	if ft.stopped || ft.fired {
		fc.mu.Unlock()
		return false
	}
	ft.stopped = true
	// Remove from the pending list atomically within the same lock.
	filtered := fc.timers[:0]
	for _, t := range fc.timers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.timers = filtered
	fc.mu.Unlock()
	return true
}

// Reset implements distlock.Timer. It re-arms the timer to fire after d.
//
// All reads and writes to stopped/fired are performed under fc.mu to avoid
// races with Stop(), PendingTimers(), and Advance(), which all use fc.mu.
func (ft *FakeTimer) Reset(d time.Duration) bool {
	fc := ft.clock

	// Remove from pending list and snapshot wasActive under fc.mu.
	fc.mu.Lock()
	wasActive := !ft.stopped && !ft.fired
	ft.stopped = false
	ft.fired = false
	now := fc.now
	ft.deadline = now.Add(d)
	// Remove from timer list (was removed by Stop if already stopped, but
	// removeTimer is idempotent).
	filtered := fc.timers[:0]
	for _, t := range fc.timers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.timers = filtered

	if d <= 0 {
		ft.fired = true
	} else {
		fc.timers = append(fc.timers, ft)
	}
	fc.mu.Unlock()

	// Drain any stale value and optionally send immediate fire — outside lock.
	select {
	case <-ft.ch:
	default:
	}
	if d <= 0 {
		select {
		case ft.ch <- now:
		default:
		}
	}
	return wasActive
}
