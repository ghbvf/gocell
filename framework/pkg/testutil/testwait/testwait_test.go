// Package testwait — self-tests covering External (synchronous polling) and
// Deterministic (channel-blocking) typed markers.
//
// External properties under test:
//   - returns immediately when condition is already true
//   - polls until condition flips, then returns
//   - times out and reports reason via t.Fatalf
//   - spawns NO per-tick goroutine (race-window fix; testify #1611/#865)
//   - propagates condition panics to the caller goroutine
//
// Deterministic properties under test:
//   - returns received value on signal arrival
//   - times out and returns zero value on silent signal
//   - generic parameterization works for chan struct{} and chan *Foo
package testwait_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// TestMain delegates goroutine-leak detection to goleak.VerifyTestMain,
// which checks for unexpected goroutines after all tests in the package
// complete. This replaces the manual runtime.NumGoroutine before/after
// pattern which was prone to scheduling noise.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeT records t.Fatalf invocations without aborting the test, letting us
// assert that External / Deterministic fail in the expected scenarios.
type fakeT struct {
	*testing.T
	failed   atomic.Bool
	lastArgs []any
	lastMsg  string
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed.Store(true)
	f.lastMsg = format
	f.lastArgs = args
	// Do NOT call the embedded T's Fatalf — we want to observe the fail.
}

func (f *fakeT) Helper() { /* swallow */ }

func TestExternal_ReturnsWhenConditionTrue(t *testing.T) {
	t.Parallel()
	start := time.Now()
	testwait.External(t, "condition-true-immediately",
		func() bool { return true },
		testtime.EventuallyShort, testtime.D10ms,
		"unreachable")
	require.Less(t, time.Since(start), testtime.D100ms,
		"External should return immediately on initial true condition")
}

func TestExternal_PollsUntilConditionFlips(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	flipAt := int32(3)
	testwait.External(t, "condition-flips-after-n-ticks",
		func() bool { return counter.Add(1) >= flipAt },
		testtime.EventuallyShort, testtime.D5ms,
		"flip target=%d", flipAt)
	require.GreaterOrEqual(t, counter.Load(), flipAt,
		"External should poll until flip")
}

func TestExternal_TimesOutAndReportsReason(t *testing.T) {
	t.Parallel()
	ft := &fakeT{T: t}
	testwait.External(ft, "always-false-times-out",
		func() bool { return false },
		testtime.D50ms, testtime.D5ms,
		"target", 42)
	require.True(t, ft.failed.Load(), "expected t.Fatalf to fire on timeout")
	// Failure message must surface the reason literal so engineers can grep
	// CI logs back to the originating callsite. fakeT preserves format + args
	// separately; rendering them with fmt.Sprintf gives the user-visible text.
	rendered := fmt.Sprintf(ft.lastMsg, ft.lastArgs...)
	require.Contains(t, rendered, "always-false-times-out",
		"failure message must include reason literal; got %q", rendered)
}

// TestExternal_TimeoutFiresWhenTickExceedsTimeout verifies that the timeout
// fires promptly even when tick > timeout. Previously the for-range-ticker.C
// loop would not check the deadline until the next tick arrived, causing
// false-green results. The separate timer in select ensures timeout fires
// within ~timeout, not ~tick.
func TestExternal_TimeoutFiresWhenTickExceedsTimeout(t *testing.T) {
	t.Parallel()
	ft := &fakeT{T: t}
	start := time.Now()
	// tick=100ms > timeout=10ms: with the old loop the test would block for
	// ~100ms before declaring timeout; with the timer+select it fires at ~10ms.
	testwait.External(ft, "tick-exceeds-timeout",
		func() bool { return false },
		testtime.D10ms, testtime.D100ms,
		"tick exceeds timeout probe")
	elapsed := time.Since(start)
	require.True(t, ft.failed.Load(), "expected t.Fatalf to fire when condition always false")
	require.Less(t, elapsed, testtime.D80ms,
		"timeout should fire ~10ms (not ~100ms tick); elapsed=%v", elapsed)
}

// TestExternal_ConditionFlipAfterDeadlineFailsAsTimeout verifies that a
// condition that flips to true AFTER the deadline causes External to call
// t.Fatalf, not return success. With the old ticker loop the condition could
// be checked after the deadline had passed, masking real timeouts.
func TestExternal_ConditionFlipAfterDeadlineFailsAsTimeout(t *testing.T) {
	t.Parallel()
	ft := &fakeT{T: t}
	// condition becomes true after 30ms; timeout is 10ms.
	// External must report timeout, not success.
	flipAt := time.Now().Add(testtime.D30ms)
	testwait.External(ft, "condition-flip-after-deadline",
		func() bool { return time.Now().After(flipAt) },
		testtime.D10ms, testtime.D2ms,
		"condition flips after deadline")
	require.True(t, ft.failed.Load(),
		"External must call t.Fatalf when condition only becomes true after timeout fires")
}

func TestExternal_NoGoroutineLeakAfterTimeout(t *testing.T) {
	t.Parallel()
	// Drive 50 timeouts back to back. Goroutine-leak detection is delegated to
	// goleak.VerifyTestMain at package teardown — see TestMain above.
	for range 50 {
		ft := &fakeT{T: t}
		testwait.External(ft, "leak-probe",
			func() bool { return false },
			testtime.D5ms, testtime.D1ms,
			"leak probe")
	}
}

func TestExternal_ConditionPanicPropagates(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		require.Equal(t, "boom-from-condition", r,
			"condition panic must propagate to the caller goroutine, not be swallowed")
	}()
	testwait.External(t, "condition-panics",
		func() bool { panic("boom-from-condition") },
		testtime.EventuallyShort, testtime.D5ms,
		"panic propagation")
}

func TestDeterministic_ReceivesValueFromSignal(t *testing.T) {
	t.Parallel()
	sig := make(chan int, 1)
	sig <- 42
	got := testwait.Deterministic(t, sig, "expected 42")
	require.Equal(t, 42, got)
}

// TestDeterministic_SafetyNetExpiresWhenSilent exercises the hung-test
// safety-net branch via the white-box forwarder with a short budget, so the
// expiry path is covered without waiting the production signalSafetyNet (30s).
// A genuinely hung signal must call t.Fatalf with the label, not block forever.
func TestDeterministic_SafetyNetExpiresWhenSilent(t *testing.T) {
	t.Parallel()
	sig := make(chan int) // never sent on
	ft := &fakeT{T: t}
	got := testwait.DeterministicWithinForTest(ft, sig, testtime.D5ms, "silent-signal")
	require.True(t, ft.failed.Load(),
		"safety-net expiry must call t.Fatalf when the signal never arrives")
	rendered := fmt.Sprintf(ft.lastMsg, ft.lastArgs...)
	require.Contains(t, rendered, "silent-signal",
		"safety-net Fatalf must echo the label for CI grep-ability; got %q", rendered)
	require.Equal(t, 0, got, "safety-net expiry must return the zero value of T")
}

func TestDeterministic_GenericOverStructSignal(t *testing.T) {
	t.Parallel()
	sig := make(chan struct{}, 1)
	sig <- struct{}{}
	got := testwait.Deterministic(t, sig, "struct signal")
	require.Equal(t, struct{}{}, got)
}

type fooPayload struct{ ID int }

// TestDeterministic_ClosedChannelReturnsImmediately documents Go channel
// semantics: receiving from a closed channel returns the zero value immediately,
// not a timeout. Deterministic must not call t.Fatalf in this case.
//
// Semantic distinction: a closed channel and an expired safety-net both return
// the zero value of T, but they are opposite outcomes. Closed channel is a
// well-formed immediate return — the producer has signaled completion by
// closing. Safety-net expiry is a hung-test failure — the producer never sent
// and Deterministic calls t.Fatalf. The assertion `ft.failed == false` is the
// load-bearing guard that distinguishes these two zero-value paths: it proves
// Deterministic treated the closed channel as a successful receive, not as a
// hung-test.
func TestDeterministic_ClosedChannelReturnsImmediately(t *testing.T) {
	t.Parallel()
	sig := make(chan int)
	close(sig)
	ft := &fakeT{T: t}
	got := testwait.Deterministic(ft, sig, "closed-channel")
	require.False(t, ft.failed.Load(),
		"Deterministic must not call t.Fatalf when channel is already closed")
	require.Equal(t, 0, got, "receive from closed chan int must return zero value")
}

func TestDeterministic_GenericOverTypedSignal(t *testing.T) {
	t.Parallel()
	sig := make(chan *fooPayload, 1)
	want := &fooPayload{ID: 7}
	sig <- want
	got := testwait.Deterministic(t, sig, "typed signal")
	require.Same(t, want, got, "Deterministic must return the exact value received")
}
