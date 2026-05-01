package clock

import "time"

// Clock is the canonical time source for GoCell production code.
//
// All methods must be safe for concurrent use.
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// Since returns the time elapsed since t. It is shorthand for Now().Sub(t).
	Since(t time.Time) time.Duration

	// Until returns the duration until t. It is shorthand for t.Sub(Now()).
	Until(t time.Time) time.Duration

	// NewTimerAt creates a [Timer] that fires when the clock reaches deadline.
	//
	// The absolute-deadline shape eliminates the read-then-act gap that a
	// duration-based API would require. Callers wanting "fire after d" should
	// write c.NewTimerAt(c.Now().Add(d)).
	NewTimerAt(deadline time.Time) Timer
}

// Timer is a single-fire timer created by a [Clock].
type Timer interface {
	// C returns the channel that receives a single value when the timer fires.
	C() <-chan time.Time

	// Stop prevents the timer from firing. It returns true if the call stops
	// the timer, false if the timer has already expired or been stopped.
	Stop() bool

	// Reset re-arms the timer to fire after d. It returns true if the timer
	// was active when Reset was called, false if it had already expired or
	// been stopped.
	Reset(d time.Duration) bool
}

// Real returns a [Clock] backed by the standard library's wall-clock time
// source. The returned value is safe for concurrent use and is the only
// production [Clock] implementation; all production wiring routes through it.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (realClock) Until(t time.Time) time.Duration { return time.Until(t) }
func (realClock) NewTimerAt(deadline time.Time) Timer {
	// time.Until handles past deadlines gracefully — time.NewTimer with a
	// non-positive duration fires immediately on the next tick of its goroutine.
	return &realTimer{t: time.NewTimer(time.Until(deadline))}
}

type realTimer struct{ t *time.Timer }

func (rt *realTimer) C() <-chan time.Time        { return rt.t.C }
func (rt *realTimer) Stop() bool                 { return rt.t.Stop() }
func (rt *realTimer) Reset(d time.Duration) bool { return rt.t.Reset(d) }
