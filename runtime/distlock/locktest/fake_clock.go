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

// NewTimer implements distlock.Clock.
// The returned FakeTimer fires when Advance moves past its deadline.
func (fc *FakeClock) NewTimer(d time.Duration) distlock.Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &FakeTimer{
		deadline: fc.now.Add(d),
		ch:       make(chan time.Time, 1),
		clock:    fc,
	}
	// If d <= 0 fire immediately.
	if d <= 0 {
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

	// Collect due timers.
	var due []*FakeTimer
	var remaining []*FakeTimer
	for _, t := range fc.timers {
		if t.stopped {
			continue
		}
		if !t.fired && !now.Before(t.deadline) {
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	fc.timers = remaining
	fc.mu.Unlock()

	// Fire outside the lock to avoid deadlock when timer receivers call back
	// into the clock (e.g. FakeClock.Now()).
	for _, t := range due {
		t.mu.Lock()
		if !t.stopped && !t.fired {
			t.fired = true
			// Non-blocking send — channel is buffered (cap 1).
			select {
			case t.ch <- now:
			default:
			}
		}
		t.mu.Unlock()
	}
}

// NowTime is a convenience accessor returning the current fake time.
func (fc *FakeClock) NowTime() time.Time { return fc.Now() }

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

// removeTimer removes a timer from the pending list (called by Stop/Reset).
// Caller must NOT hold fc.mu.
func (fc *FakeClock) removeTimer(ft *FakeTimer) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	filtered := fc.timers[:0]
	for _, t := range fc.timers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.timers = filtered
}

// addTimer re-registers a timer (called by Reset).
// Caller must NOT hold fc.mu.
func (fc *FakeClock) addTimer(ft *FakeTimer) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.timers = append(fc.timers, ft)
}

// FakeTimer is a single-fire timer controlled by FakeClock.
type FakeTimer struct {
	mu       sync.Mutex
	deadline time.Time
	ch       chan time.Time
	clock    *FakeClock
	stopped  bool
	fired    bool
}

// C implements distlock.Timer.
func (ft *FakeTimer) C() <-chan time.Time { return ft.ch }

// Stop implements distlock.Timer.
func (ft *FakeTimer) Stop() bool {
	ft.clock.removeTimer(ft)
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.stopped || ft.fired {
		return false
	}
	ft.stopped = true
	return true
}

// Reset implements distlock.Timer. It re-arms the timer to fire after d.
func (ft *FakeTimer) Reset(d time.Duration) bool {
	ft.clock.removeTimer(ft)

	ft.mu.Lock()
	wasActive := !ft.stopped && !ft.fired
	ft.stopped = false
	ft.fired = false
	// Drain the channel if there's a stale value.
	select {
	case <-ft.ch:
	default:
	}
	fc := ft.clock
	now := fc.Now()
	ft.deadline = now.Add(d)
	ft.mu.Unlock()

	if d <= 0 {
		ft.mu.Lock()
		ft.fired = true
		ft.mu.Unlock()
		select {
		case ft.ch <- now:
		default:
		}
	} else {
		ft.clock.addTimer(ft)
	}
	return wasActive
}
