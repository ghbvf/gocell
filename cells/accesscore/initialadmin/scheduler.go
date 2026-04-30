package initialadmin

import "time"

// Cancellable represents a scheduled task that can be canceled before it fires.
type Cancellable interface {
	// Stop prevents the scheduled function from firing. Returns true if the
	// call stops the timer before it fires, false if it has already fired or
	// been stopped.
	Stop() bool
}

// Scheduler abstracts time.AfterFunc to allow deterministic testing.
// Production code uses realScheduler{}; tests may inject a fakeScheduler.
type Scheduler interface {
	// AfterFunc schedules fn to run after duration d in its own goroutine.
	// The returned Cancellable can be used to prevent fn from running.
	AfterFunc(d time.Duration, fn func()) Cancellable
}

// realScheduler implements Scheduler using time.AfterFunc.
type realScheduler struct{}

// AfterFunc delegates to the stdlib time.AfterFunc.
func (realScheduler) AfterFunc(d time.Duration, fn func()) Cancellable {
	return time.AfterFunc(d, fn)
}
