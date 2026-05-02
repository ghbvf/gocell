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
	"context"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

// Compile-time assertions.
var (
	_ clock.Clock  = (*FakeClock)(nil)
	_ clock.Timer  = (*FakeTimer)(nil)
	_ clock.Ticker = (*FakeTicker)(nil)
)

// FakeClock is a controllable [clock.Clock] for deterministic testing. Time
// advances only when [FakeClock.Advance] or [FakeClock.Set] is called; all
// timers and tickers created by this clock fire synchronously when the clock
// passes their deadline.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*FakeTimer
	tickers []*FakeTicker
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

// NewTicker implements [clock.Clock]. The returned [FakeTicker] fires every
// interval starting from Now()+interval, mirroring stdlib time.NewTicker
// first-fire semantics. Advance / Set may fire multiple ticks worth of
// elapsed intervals; the channel is buffered to 1 so excess ticks coalesce
// (also matching stdlib).
func (fc *FakeClock) NewTicker(interval time.Duration) clock.Ticker {
	clock.MustHavePositiveInterval(interval, "clockmock.FakeClock.NewTicker")
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &FakeTicker{
		interval: interval,
		nextFire: fc.now.Add(interval),
		ch:       make(chan time.Time, 1),
		clock:    fc,
	}
	fc.tickers = append(fc.tickers, ft)
	return ft
}

// AfterFunc implements [clock.Clock]. fn is invoked on its own goroutine the
// first time Advance / Set causes the clock to reach deadline. Past deadlines
// schedule fn immediately. The returned timer's C channel never receives a
// value; only Stop / Reset are meaningful for AfterFunc timers.
func (fc *FakeClock) AfterFunc(deadline time.Time, fn func()) clock.Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &FakeTimer{
		deadline: deadline,
		ch:       make(chan time.Time, 1),
		clock:    fc,
		callback: fn,
	}
	if !fc.now.Before(deadline) {
		ft.fired = true
		go fn()
	} else {
		fc.timers = append(fc.timers, ft)
	}
	return ft
}

// Sleep implements [clock.Clock]. Blocks until ctx is canceled or the fake
// clock is advanced past until, whichever happens first. Returns ctx.Err()
// on cancelation, nil otherwise.
func (fc *FakeClock) Sleep(ctx context.Context, until time.Time) error {
	if !fc.Now().Before(until) {
		return ctx.Err()
	}
	timer := fc.NewTimerAt(until)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}

// Advance moves the clock forward by d and fires all timers / tickers whose
// deadline has passed. Returns after every due timer's channel has been
// written and every due AfterFunc callback has been launched.
func (fc *FakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	fc.fireDueAndUnlock()
}

// Set moves the clock to t (forward or backward) and fires all timers /
// tickers whose deadline has passed. Backward moves do not un-fire
// already-fired timers.
func (fc *FakeClock) Set(t time.Time) {
	fc.mu.Lock()
	fc.now = t
	fc.fireDueAndUnlock()
}

// tickerFire pairs a FakeTicker with the time at which it fires.
type tickerFire struct {
	ticker *FakeTicker
	at     time.Time
}

// collectDueTimers partitions fc.timers into due (fired) and remaining (still
// pending) slices. Must be called with fc.mu held.
func (fc *FakeClock) collectDueTimers(now time.Time) (due, remaining []*FakeTimer) {
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
	return due, remaining
}

// collectDueTickers returns all tickers whose next fire time has passed and
// advances each ticker's nextFire to the next boundary after now. Must be
// called with fc.mu held.
func (fc *FakeClock) collectDueTickers(now time.Time) []tickerFire {
	var due []tickerFire
	for _, tk := range fc.tickers {
		if tk.stopped {
			continue
		}
		if !tk.nextFire.After(now) {
			due = append(due, tickerFire{ticker: tk, at: now})
			// Catch up nextFire to the next interval boundary strictly after now.
			for !tk.nextFire.After(now) {
				tk.nextFire = tk.nextFire.Add(tk.interval)
			}
		}
	}
	return due
}

// sendDueTimers delivers timer events to their channels or callbacks. Must be
// called with fc.mu released (channel send + AfterFunc launch run unlocked to
// avoid deadlocking handlers that call back into the clock).
//
// Each delivery briefly re-acquires fc.mu to verify t.fired is still set: a
// concurrent FakeTimer.Reset may have cleared the fired flag and re-armed
// this timer between collectDueTimers (under lock) and the iteration here.
// Without this guard, a stale "fire" event collected before Reset could
// deliver onto the freshly-armed channel, surfacing as a phantom tick to
// observers — the I-05 race symptom.
func sendDueTimers(fc *FakeClock, now time.Time, dueTimers []*FakeTimer) {
	for _, t := range dueTimers {
		if t.callback != nil {
			go t.callback()
			continue
		}
		fc.mu.Lock()
		stillFired := t.fired
		fc.mu.Unlock()
		if !stillFired {
			continue
		}
		select {
		case t.ch <- now:
		default:
		}
	}
}

// sendDueTickers delivers ticker events to their channels. Must be called with
// fc.mu released.
func sendDueTickers(dueTickers []tickerFire) {
	for _, tf := range dueTickers {
		select {
		case tf.ticker.ch <- tf.at:
		default:
		}
	}
}

// fireDueAndUnlock must be called with fc.mu held; it releases fc.mu before
// sending to timer channels and launching AfterFunc callbacks to avoid
// deadlock when receivers call back into the clock.
//
// The split is intentional: collectDueTimers / collectDueTickers run under
// lock to compute the snapshot atomically; the unlock happens before the
// channel sends so that handler callbacks that re-enter the clock (e.g.
// observers calling NewTimerAt or Stop) don't deadlock. sendDueTimers
// re-acquires fc.mu briefly per-timer to validate the fired flag against
// concurrent Reset (see sendDueTimers doc).
func (fc *FakeClock) fireDueAndUnlock() {
	now := fc.now
	dueTimers, remainingTimers := fc.collectDueTimers(now)
	fc.timers = remainingTimers
	dueTickers := fc.collectDueTickers(now)
	fc.mu.Unlock()

	sendDueTimers(fc, now, dueTimers)
	sendDueTickers(dueTickers)
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

// PendingTickers returns the number of active (non-stopped) tickers.
func (fc *FakeClock) PendingTickers() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	count := 0
	for _, tk := range fc.tickers {
		if !tk.stopped {
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
	callback func() // non-nil for AfterFunc timers; runs on a fresh goroutine when the timer fires
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
//
// The channel drain and re-arm are performed while fc.mu is held so that a
// concurrent sendDueTimers cannot deliver a new tick between the drain and
// the re-arm, eliminating the I-05 race window.
func (ft *FakeTimer) Reset(d time.Duration) bool {
	fc := ft.clock

	fc.mu.Lock()
	wasActive := !ft.stopped && !ft.fired
	ft.stopped = false
	ft.fired = false
	now := fc.now
	ft.deadline = now.Add(d)

	// Remove ft from the pending timer list (it will be re-added below if needed).
	filtered := fc.timers[:0]
	for _, t := range fc.timers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.timers = filtered

	// Drain a stale buffered tick while holding the lock. The drain handles
	// the case where a prior Advance landed a tick into ft.ch (cap 1) while
	// the timer was still considered "fired"; the in-flight sendDueTimers
	// path is separately guarded by a fired-flag re-check (see sendDueTimers)
	// so a concurrent send observes ft.fired == false (cleared above) and
	// skips the stale delivery. Together these eliminate the I-05 race.
	select {
	case <-ft.ch:
	default:
	}

	immediate := false
	if d <= 0 {
		ft.fired = true
		immediate = true
	} else {
		fc.timers = append(fc.timers, ft)
	}
	callback := ft.callback
	fc.mu.Unlock()

	if immediate {
		if callback != nil {
			go callback()
		} else {
			select {
			case ft.ch <- now:
			default:
			}
		}
	}
	return wasActive
}

// ResetAt implements [clock.Timer]. Re-arms the timer to fire at the given
// absolute deadline. Uses the same locked-drain pattern as Reset to avoid
// the I-05 race window.
func (ft *FakeTimer) ResetAt(deadline time.Time) bool {
	fc := ft.clock

	fc.mu.Lock()
	wasActive := !ft.stopped && !ft.fired
	ft.stopped = false
	ft.fired = false
	now := fc.now
	ft.deadline = deadline

	// Remove ft from the pending timer list.
	filtered := fc.timers[:0]
	for _, t := range fc.timers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.timers = filtered

	// Drain a stale tick while holding the lock.
	select {
	case <-ft.ch:
	default:
	}

	immediate := false
	if !now.Before(deadline) {
		ft.fired = true
		immediate = true
	} else {
		fc.timers = append(fc.timers, ft)
	}
	callback := ft.callback
	fc.mu.Unlock()

	if immediate {
		if callback != nil {
			go callback()
		} else {
			select {
			case ft.ch <- now:
			default:
			}
		}
	}
	return wasActive
}

// FakeTicker is a periodic ticker controlled by a [FakeClock]. Mutable
// fields are protected by the parent FakeClock's mu.
type FakeTicker struct {
	interval time.Duration
	nextFire time.Time
	ch       chan time.Time
	clock    *FakeClock
	stopped  bool
}

// C implements [clock.Ticker].
func (ft *FakeTicker) C() <-chan time.Time { return ft.ch }

// Stop implements [clock.Ticker]. Stop is idempotent.
func (ft *FakeTicker) Stop() {
	fc := ft.clock
	fc.mu.Lock()
	if ft.stopped {
		fc.mu.Unlock()
		return
	}
	ft.stopped = true
	filtered := fc.tickers[:0]
	for _, t := range fc.tickers {
		if t != ft {
			filtered = append(filtered, t)
		}
	}
	fc.tickers = filtered
	fc.mu.Unlock()
}

// Reset implements [clock.Ticker]. Resets interval and computes the next
// fire time as Now()+interval (matching stdlib semantics).
func (ft *FakeTicker) Reset(interval time.Duration) {
	clock.MustHavePositiveInterval(interval, "clockmock.FakeTicker.Reset")
	fc := ft.clock
	fc.mu.Lock()
	ft.interval = interval
	ft.nextFire = fc.now.Add(interval)
	if ft.stopped {
		ft.stopped = false
		fc.tickers = append(fc.tickers, ft)
	}
	fc.mu.Unlock()
}
