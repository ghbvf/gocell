package distlock

import "time"

// Clock abstracts wall-clock access for distlock package internals so tests can
// control time deterministically. This discipline applies only within distlock;
// callers may use time.Now() freely in their own code.
//
// ref: golang stdlib time package — same method signatures, injectable interface
// ref: plan "时间纪律" section — all timing flows through Clock; no literal waits
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTimerAt creates a timer that fires when the clock reaches the given
	// absolute deadline. If deadline is at or before Now(), the timer fires
	// immediately.
	//
	// Absolute-deadline (rather than duration) eliminates a read-then-act gap
	// in callers that derive the deadline from heap state ahead of time: the
	// renewal heap stores absolute nextRenew times, so a deadline-based API
	// lets the caller pass the value through unchanged. With a duration API,
	// the caller would compute d = deadline - clock.Now() and then pass d to
	// NewTimer(d); under FakeClock, an Advance() interleaving between the
	// two non-atomic calls would re-baseline the timer to deadline + advance
	// delta — the root cause of the TC-3 flake.
	NewTimerAt(deadline time.Time) Timer
	// Since returns the elapsed time since t, equivalent to Now().Sub(t).
	Since(t time.Time) time.Duration
}

// Timer abstracts a single-fire timer so FakeClock can control expiry.
type Timer interface {
	// C returns the channel on which the timer delivers its value.
	C() <-chan time.Time
	// Stop prevents the timer from firing. Returns true if the call stops
	// the timer, false if the timer has already expired or been stopped.
	Stop() bool
	// Reset changes the timer to expire after duration d.
	// It returns true if the timer had been active, false if it had fired
	// or been stopped.
	Reset(d time.Duration) bool
}

// RealClockForTest returns a Clock backed by the real wall clock. It is
// exported for use in clock_test.go to test realClock behavior through the
// Clock interface without making realClock itself public.
func RealClockForTest() Clock {
	return realClock{}
}

// realClock is the production Clock backed by the standard library.
type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (realClock) NewTimerAt(deadline time.Time) Timer {
	// time.NewTimer normalizes negative durations to fire-immediately, so we
	// can pass time.Until(deadline) without a guard.
	return &realTimer{t: time.NewTimer(time.Until(deadline))}
}

// realTimer wraps *time.Timer to satisfy the Timer interface.
type realTimer struct {
	t *time.Timer
}

func (r *realTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
