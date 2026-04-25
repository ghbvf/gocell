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
	// NewTimer creates a timer that fires after duration d.
	// If d <= 0 the timer fires immediately.
	NewTimer(d time.Duration) Timer
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

// realClock is the production Clock backed by the standard library.
type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (realClock) NewTimer(d time.Duration) Timer  { return &realTimer{t: time.NewTimer(d)} }

// realTimer wraps *time.Timer to satisfy the Timer interface.
type realTimer struct {
	t *time.Timer
}

func (r *realTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
