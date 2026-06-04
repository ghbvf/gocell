package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// tickerInterval is the fake-clock interval used by the ticker tests. Its
// wall-clock magnitude is irrelevant — cadence is driven by FakeClock.Advance,
// not real time — but it must be positive so construction does not panic.
const tickerInterval = testtime.D1s

// -----------------------------------------------------------------------------
// TickerTrigger
// -----------------------------------------------------------------------------

// TestTickerTrigger_EmitsAtInterval drives the trigger off an injected fake
// clock: each Advance(interval) fires exactly one zero-value resync pulse, with
// no dependence on wall-clock time.
func TestTickerTrigger_EmitsAtInterval(t *testing.T) {
	fc := clockmock.New(time.Time{})
	trig := TickerTrigger(fc, tickerInterval)

	queue := make(chan Request, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start registers the ticker synchronously before returning, so the Advance
	// below deterministically fires it (no startup race).
	require.NoError(t, trig.Start(ctx, queue))

	fc.Advance(tickerInterval)
	got := testwait.Deterministic(t, queue, "ticker-pulse-1")
	assert.Equal(t, Request{}, got, "TickerTrigger emits a zero-value resync pulse")

	fc.Advance(tickerInterval)
	got2 := testwait.Deterministic(t, queue, "ticker-pulse-2")
	assert.Equal(t, Request{}, got2, "second interval emits a second pulse")
}

// TestTickerTrigger_RespectsCtxCancel asserts the ticker goroutine exits on ctx
// cancellation (goleak proves no leak).
func TestTickerTrigger_RespectsCtxCancel(t *testing.T) {
	fc := clockmock.New(time.Time{})
	trig := TickerTrigger(fc, tickerInterval)

	queue := make(chan Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, trig.Start(ctx, queue))
	cancel() // goroutine must observe ctx.Done() and return
}

// TestTickerTrigger_NilClockPanics asserts construction fails fast on a nil
// clock (clock.MustHaveClock), not at Start.
func TestTickerTrigger_NilClockPanics(t *testing.T) {
	assert.Panics(t, func() { TickerTrigger(nil, tickerInterval) })
}

// TestTickerTrigger_NonPositiveIntervalPanics asserts construction fails fast on
// a non-positive interval (clock.MustHavePositiveInterval). Both boundary cases
// of d <= 0 are checked: a negative interval and the zero value.
func TestTickerTrigger_NonPositiveIntervalPanics(t *testing.T) {
	fc := clockmock.New(time.Time{})
	for _, interval := range []time.Duration{testtime.DNeg1s, 0} {
		interval := interval
		assert.Panics(t, func() { TickerTrigger(fc, interval) },
			"non-positive interval %v must panic at construction", interval)
	}
}

// -----------------------------------------------------------------------------
// ChannelTrigger
// -----------------------------------------------------------------------------

// TestChannelTrigger_PassesRequest asserts an external Request written to the
// source channel is forwarded to the work queue with its EntityID intact.
func TestChannelTrigger_PassesRequest(t *testing.T) {
	in := make(chan Request, 1)
	trig := ChannelTrigger(in)

	queue := make(chan Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, trig.Start(ctx, queue))

	in <- Request{EntityID: "x"}
	got := testwait.Deterministic(t, queue, "channel-forward")
	assert.Equal(t, "x", got.EntityID)
}

// TestChannelTrigger_BlockedOnFull proves block-don't-drop backpressure: with an
// unbuffered work queue, the trigger forwards every Request in order, blocking
// on each send until the consumer reads it — nothing is dropped.
func TestChannelTrigger_BlockedOnFull(t *testing.T) {
	in := make(chan Request)
	trig := ChannelTrigger(in)

	queue := make(chan Request) // unbuffered: each forward blocks until read
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, trig.Start(ctx, queue))

	go func() {
		in <- Request{EntityID: "a"}
		in <- Request{EntityID: "b"}
		in <- Request{EntityID: "c"}
	}()

	assert.Equal(t, "a", testwait.Deterministic(t, queue, "bp-a").EntityID)
	assert.Equal(t, "b", testwait.Deterministic(t, queue, "bp-b").EntityID)
	assert.Equal(t, "c", testwait.Deterministic(t, queue, "bp-c").EntityID)
}

// TestChannelTrigger_SourceClosedExits asserts the goroutine exits cleanly when
// the source channel is closed (no leak, no further forwarding).
func TestChannelTrigger_SourceClosedExits(t *testing.T) {
	in := make(chan Request)
	trig := ChannelTrigger(in)

	queue := make(chan Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, trig.Start(ctx, queue))
	close(in) // source closed → goroutine returns
}

// TestChannelTrigger_RespectsCtxCancel asserts the goroutine exits on ctx
// cancellation while idle (blocked on the source) — the outer select's ctx.Done
// path (goleak proves no leak).
func TestChannelTrigger_RespectsCtxCancel(t *testing.T) {
	in := make(chan Request)
	trig := ChannelTrigger(in)

	queue := make(chan Request)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, trig.Start(ctx, queue))
	cancel()
}

// TestChannelTrigger_CancelWhileBlockedOnQueue exercises the INNER select's
// ctx.Done path: the goroutine has a Request in hand and is blocked forwarding
// it to an unread queue when ctx is canceled. The unbuffered source handshake
// (in <- req completes only after the goroutine receives) guarantees the
// goroutine is committed to the inner `queue <- req` select — which can never
// proceed (no reader) — so cancel deterministically drives the inner exit (no
// leak, no hang).
func TestChannelTrigger_CancelWhileBlockedOnQueue(t *testing.T) {
	in := make(chan Request) // unbuffered: send completes after the goroutine receives
	trig := ChannelTrigger(in)

	queue := make(chan Request) // unbuffered, never read → the forward blocks
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, trig.Start(ctx, queue))

	in <- Request{EntityID: "stuck"}
	cancel()
}

// TestChannelTrigger_NilSourcePanics asserts construction fails fast on a nil
// source channel (a nil channel would block forever — a programmer error).
func TestChannelTrigger_NilSourcePanics(t *testing.T) {
	assert.Panics(t, func() { ChannelTrigger(nil) })
}
