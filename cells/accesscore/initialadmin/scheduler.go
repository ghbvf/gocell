package initialadmin

import (
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

// Cancellable represents a scheduled task that can be canceled before it fires.
type Cancellable interface {
	// Stop prevents the scheduled function from firing. Returns true if the
	// call stops the timer before it fires, false if it has already fired or
	// been stopped.
	Stop() bool
}

// Scheduler abstracts AfterFunc to allow deterministic testing.
// Production code uses newRealScheduler(clk); tests may inject a fakeScheduler.
type Scheduler interface {
	// AfterFunc schedules fn to run after duration d in its own goroutine.
	// The returned Cancellable can be used to prevent fn from running.
	AfterFunc(d time.Duration, fn func()) Cancellable
}

// realScheduler implements Scheduler using an injected clock.Clock.
type realScheduler struct {
	clk clock.Clock
}

// newRealScheduler constructs a realScheduler backed by the given clock.
func newRealScheduler(clk clock.Clock) Scheduler {
	clock.MustHaveClock(clk, "initialadmin.newRealScheduler")
	return realScheduler{clk: clk}
}

// AfterFunc delegates to clk.AfterFunc converting duration to absolute deadline.
func (s realScheduler) AfterFunc(d time.Duration, fn func()) Cancellable {
	return s.clk.AfterFunc(s.clk.Now().Add(d), fn)
}
