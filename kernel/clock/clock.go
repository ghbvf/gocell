package clock

import (
	"context"
	"time"
)

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

	// NewTicker creates a [Ticker] that fires every interval.
	//
	// First fire is at Now()+interval, matching stdlib time.NewTicker
	// semantics. interval must be > 0; non-positive values panic with the
	// same message as time.NewTicker.
	//
	// Callers must call Ticker.Stop when done; the fake implementation also
	// requires Stop to release internal references.
	NewTicker(interval time.Duration) Ticker

	// AfterFunc schedules fn to run when the clock reaches deadline. The
	// returned [Timer] can be used to cancel the callback (Stop returns true
	// if cancelation prevented the run) or to re-arm it (Reset).
	//
	// fn runs on its own goroutine and must not block the clock. Past
	// deadlines schedule fn for immediate execution.
	AfterFunc(deadline time.Time, fn func()) Timer

	// Sleep blocks until the clock reaches until or ctx is canceled,
	// whichever comes first. Returns ctx.Err() on cancelation, nil
	// otherwise. A non-positive remaining duration returns immediately
	// (ctx.Err() if ctx is already done, nil otherwise).
	Sleep(ctx context.Context, until time.Time) error
}

// Timer is a single-fire timer created by a [Clock].
type Timer interface {
	// C returns the channel that receives a single value when the timer fires.
	//
	// AfterFunc-created timers do not deliver on C; their callback is
	// invoked on a separate goroutine. C is still safe to read but will
	// never receive a value.
	C() <-chan time.Time

	// Stop prevents the timer from firing. It returns true if the call stops
	// the timer, false if the timer has already expired or been stopped.
	Stop() bool

	// Reset re-arms the timer to fire after d. It returns true if the timer
	// was active when Reset was called, false if it had already expired or
	// been stopped.
	Reset(d time.Duration) bool
}

// Ticker fires repeatedly at a fixed interval, mirroring stdlib time.Ticker.
type Ticker interface {
	// C returns the channel that receives a value on each tick. The channel
	// is buffered to capacity 1; a slow receiver causes ticks to be
	// coalesced (matches stdlib time.Ticker semantics).
	C() <-chan time.Time

	// Stop halts the ticker. It must be called to release internal
	// references; failing to call Stop leaks the ticker indefinitely. Stop
	// does not close C.
	Stop()

	// Reset changes the ticker's interval. If interval is non-positive,
	// Reset panics with the same message as stdlib time.Ticker.Reset.
	Reset(interval time.Duration)
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

func (realClock) NewTicker(interval time.Duration) Ticker {
	return &realTicker{t: time.NewTicker(interval)}
}

func (realClock) AfterFunc(deadline time.Time, fn func()) Timer {
	return &realTimer{t: time.AfterFunc(time.Until(deadline), fn)}
}

func (realClock) Sleep(ctx context.Context, until time.Time) error {
	d := time.Until(until)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type realTimer struct{ t *time.Timer }

func (rt *realTimer) C() <-chan time.Time        { return rt.t.C }
func (rt *realTimer) Stop() bool                 { return rt.t.Stop() }
func (rt *realTimer) Reset(d time.Duration) bool { return rt.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (rt *realTicker) C() <-chan time.Time   { return rt.t.C }
func (rt *realTicker) Stop()                 { rt.t.Stop() }
func (rt *realTicker) Reset(d time.Duration) { rt.t.Reset(d) }
