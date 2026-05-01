// Package clockmock provides a deterministic [clock.Clock] implementation
// driven by explicit [FakeClock.Advance] and [FakeClock.Set] calls.
//
// All FakeClock methods are safe for concurrent use. Time progress is fully
// caller-controlled: [FakeClock.Now] never advances on its own, and timers
// created via [FakeClock.NewTimerAt] only fire when Advance or Set moves the
// clock past their deadline. This is the only mechanism test code should use
// to exercise time-dependent business logic in GoCell.
package clockmock

import (
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

// Compile-time assertions.
var (
	_ clock.Clock = (*FakeClock)(nil)
	_ clock.Timer = (*FakeTimer)(nil)
)

// FakeClock is a controllable [clock.Clock] for deterministic testing. Time
// advances only when [FakeClock.Advance] or [FakeClock.Set] is called; all
// timers created by this clock fire synchronously when the clock passes their
// deadline.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*FakeTimer
}

// New creates a FakeClock starting at the given initial time. If zero, the
// clock starts at a fixed UTC epoch (2024-01-01) so test fixtures remain
// deterministic across runs.
func New(initial time.Time) *FakeClock {
	if initial.IsZero() {
		initial = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: initial}
}

// Now implements [clock.Clock].
func (fc *FakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

// Since implements [clock.Clock].
func (fc *FakeClock) Since(t time.Time) time.Duration {
	return fc.Now().Sub(t)
}

// Until implements [clock.Clock].
func (fc *FakeClock) Until(t time.Time) time.Duration {
	return t.Sub(fc.Now())
}

// NewTimerAt implements [clock.Clock]. The returned [FakeTimer] fires when
// [FakeClock.Advance] or [FakeClock.Set] moves fc past deadline.
//
// The deadline write and timer registration happen under a single fc.mu hold,
// which is what makes the API race-free against concurrent Advance — a
// duration-based API would require two non-atomic clock interactions.
func (fc *FakeClock) NewTimerAt(deadline time.Time) clock.Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &FakeTimer{
		deadline: deadline,
		ch:       make(chan time.Time, 1),
		clock:    fc,
	}
	if !fc.now.Before(deadline) {
		ft.ch <- fc.now
		ft.fired = true
	} else {
		fc.timers = append(fc.timers, ft)
	}
	return ft
}

// Advance moves the clock forward by d and fires all timers whose deadline
// has passed. Returns after every due timer's channel has been written.
//
// Timers use independent buffered channels, so callers must not rely on any
// cross-timer delivery order when one Advance makes multiple timers due at
// once.
func (fc *FakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	fc.fireDueLocked()
}

// Set moves the clock to t (forward or backward) and fires all timers whose
// deadline has passed. Backward moves do not un-fire already-fired timers.
func (fc *FakeClock) Set(t time.Time) {
	fc.mu.Lock()
	fc.now = t
	fc.fireDueLocked()
}

// fireDueLocked must be called with fc.mu held; it releases fc.mu before
// sending to timer channels to avoid deadlock when receivers call back into
// the clock.
func (fc *FakeClock) fireDueLocked() {
	now := fc.now
	var due []*FakeTimer
	var remaining []*FakeTimer
	for _, t := range fc.timers {
		if t.stopped {
			continue
		}
		if !t.fired && !now.Before(t.deadline) {
			t.fired = true
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	fc.timers = remaining
	fc.mu.Unlock()

	for _, t := range due {
		select {
		case t.ch <- now:
		default:
		}
	}
}

// PendingTimers returns the number of active (non-stopped, non-fired) timers
// currently registered with this clock. Useful for syncing with goroutines
// that re-arm a timer after a tick.
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

// FakeTimer is a single-fire timer controlled by a [FakeClock]. All mutable
// fields are protected by the parent FakeClock's mu; FakeTimer itself holds
// no lock.
type FakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	clock    *FakeClock
	stopped  bool
	fired    bool
}

// C implements [clock.Timer].
func (ft *FakeTimer) C() <-chan time.Time { return ft.ch }

// Stop implements [clock.Timer]. Returns true if the timer was stopped before
// firing; false if it had already fired or been stopped.
func (ft *FakeTimer) Stop() bool {
	fc := ft.clock
	fc.mu.Lock()
	if ft.stopped || ft.fired {
		fc.mu.Unlock()
		return false
	}
	ft.stopped = true
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

// Reset implements [clock.Timer]. Returns true if the timer was active when
// Reset was called.
func (ft *FakeTimer) Reset(d time.Duration) bool {
	fc := ft.clock

	fc.mu.Lock()
	wasActive := !ft.stopped && !ft.fired
	ft.stopped = false
	ft.fired = false
	now := fc.now
	ft.deadline = now.Add(d)
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
