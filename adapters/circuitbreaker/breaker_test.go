package circuitbreaker

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/http/middleware"
)

// Compile-time checks: Adapter implements Allower and CircuitBreakerRetryAfter.
var (
	_ middleware.Allower                  = (*Adapter)(nil)
	_ middleware.CircuitBreakerRetryAfter = (*Adapter)(nil)
)

// smallDelta is a sub-nanosecond nudge used to push the fake clock just past
// an expiry boundary without aligning exactly on it. Two nanoseconds avoids
// the off-by-one in expiry.Before(now) comparisons.
const smallDelta = 2 * time.Nanosecond

// mustNew creates an Adapter with a fake clock, failing the test on error.
func mustNew(t *testing.T, cfg Config) *Adapter {
	t.Helper()
	fc := clockmock.New(time.Unix(0, 0))
	a, err := New(cfg, fc)
	require.NoError(t, err)
	return a
}

// mustNewWithClock creates an Adapter with a fake clock, returning both,
// failing the test on error.
func mustNewWithClock(t *testing.T, cfg Config) (*Adapter, *clockmock.FakeClock) {
	t.Helper()
	fc := clockmock.New(time.Unix(0, 0))
	a, err := New(cfg, fc)
	require.NoError(t, err)
	return a, fc
}

func TestAdapter_DefaultConfig_Closed(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-default"})
	allowed, done := a.Allow()
	require.True(t, allowed, "newly created breaker must be in closed state")
	require.NotNil(t, done)
	done(nil) // report success
}

func TestAdapter_OpensAfterFailures(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{
		Name: "test-open",
		// Default ReadyToTrip: consecutive failures > 5
	})

	// Trip the breaker with 6 consecutive failures.
	for range 6 {
		allowed, done := a.Allow()
		require.True(t, allowed, "breaker should still be closed during failure reporting")
		done(errors.New("failure"))
	}

	// Now the circuit should be open.
	allowed, done := a.Allow()
	assert.False(t, allowed, "breaker must be open after consecutive failures")
	assert.Nil(t, done)
}

func TestAdapter_HalfOpenAfterTimeout(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:    "test-halfopen",
		Timeout: testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	allowed, _ := a.Allow()
	require.False(t, allowed, "breaker must be open")

	// Advance clock past the timeout to trigger half-open transition.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	allowed, done := a.Allow()
	require.True(t, allowed, "must transition to half-open after Timeout")
	done(nil) // successful probe
}

func TestAdapter_ClosesAfterHalfOpenSuccess(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:    "test-close",
		Timeout: testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	// Advance clock past timeout → half-open, then successful probe → closed.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	allowed, done := a.Allow()
	require.True(t, allowed, "breaker must reach half-open")
	done(nil) // successful probe → closed

	// Should be back to closed — multiple requests allowed.
	for i := range 3 {
		allowed, done := a.Allow()
		assert.True(t, allowed, "breaker must be closed after half-open success (attempt %d)", i)
		if done != nil {
			done(nil)
		}
	}
}

func TestAdapter_OnStateChangeCallback(t *testing.T) {
	t.Parallel()
	var transitions []string
	a, fc := mustNewWithClock(t, Config{
		Name:    "test-callback",
		Timeout: testtime.D100ms,
		OnStateChange: func(name string, from, to State) {
			transitions = append(transitions, from.String()+"→"+to.String())
		},
	})

	// Trip: closed → open
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Contains(t, transitions, "closed→open")

	// Advance clock past timeout → half-open transition fires on next Allow.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	allowed, done := a.Allow()
	require.True(t, allowed, "breaker must reach half-open for callback test")
	done(nil) // successful probe → half-open → closed

	assert.Contains(t, transitions, "open→half-open")
	assert.Contains(t, transitions, "half-open→closed")
}

func TestAdapter_RetryAfter_CustomTimeout(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-retry", Timeout: testtime.D30s})
	assert.Equal(t, testtime.D30s, a.RetryAfter())
}

func TestAdapter_RetryAfter_DefaultTimeout(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-retry-default"})
	assert.Equal(t, testtime.D60s, a.RetryAfter(), "default timeout is 60s")
}

func TestAdapter_CustomReadyToTrip(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{
		Name: "test-custom-trip",
		ReadyToTrip: func(counts Counts) bool {
			// Trip after just 2 failures.
			return counts.ConsecutiveFailures > 2
		},
	})

	// 3 failures should trip.
	for range 3 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	allowed, _ := a.Allow()
	assert.False(t, allowed, "custom ReadyToTrip(>2) must trip after 3 failures")
}

func TestAdapter_State(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-state"})
	assert.Equal(t, StateClosed, a.State(), "new breaker starts in closed state")
}

func TestAdapter_Allow_SuccessNilError(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-success-nil"})
	allowed, done := a.Allow()
	require.True(t, allowed)
	require.NotNil(t, done)

	// Calling done(nil) must not panic and must count as success.
	done(nil)

	// Breaker stays closed after success.
	allowed2, _ := a.Allow()
	assert.True(t, allowed2, "breaker must stay closed after successful request")
}

func TestAdapter_Allow_FailureNonNilError(t *testing.T) {
	t.Parallel()
	a := mustNew(t, Config{Name: "test-failure-err"})
	allowed, done := a.Allow()
	require.True(t, allowed)
	require.NotNil(t, done)

	// Calling done with an error must count as failure.
	done(errors.New("upstream error"))

	// One failure is not enough to trip (default threshold > 5), still closed.
	allowed2, _ := a.Allow()
	assert.True(t, allowed2, "single failure must not trip circuit with default settings")
}

// TestNew_EmptyName_Errors verifies that New rejects an empty Name so
// production configurations are never silently misconfigured.
func TestNew_EmptyName_Errors(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(time.Unix(0, 0))
	a, err := New(Config{}, fc)
	require.Error(t, err, "empty Name must return an error")
	assert.Nil(t, a)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterCircuitBreakerConfig, ec.Code)
	assert.Contains(t, err.Error(), "Name required")
}

// TestNew_NilClock_Panics verifies that New panics on nil clock,
// consistent with PROD-CLOCK-INJECTION-01 and the ratelimit adapter pattern.
func TestNew_NilClock_Panics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_, _ = New(Config{Name: "test-nil-clock"}, nil)
	})
}

// TestAdapter_HalfOpen_MaxRequestsConcurrent verifies that MaxRequests=1 in
// half-open state allows exactly one concurrent request and rejects all others.
//
// ref: sony/gobreaker v2 twostep_breaker.go — half-open MaxRequests
func TestAdapter_HalfOpen_MaxRequestsConcurrent(t *testing.T) {
	t.Parallel()
	const concurrency = 8

	a, fc := mustNewWithClock(t, Config{
		Name:        "test-halfopen-concurrent",
		MaxRequests: 1,
		Timeout:     testtime.D50ms,
	})

	// Trip the breaker: need > 5 consecutive failures (default ReadyToTrip).
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	// Confirm the circuit is open.
	allowed, _ := a.Allow()
	require.False(t, allowed, "breaker must be open after tripping")

	// Advance clock past timeout → breaker enters half-open on next Allow.
	fc.Advance(testtime.D50ms + time.Nanosecond)

	// Probe to confirm we're in half-open; fail to reopen for concurrent test.
	probe, probeDone := a.Allow()
	require.True(t, probe, "breaker must enter half-open after fc.Advance")
	probeDone(errors.New("probe fail")) // reopen so we can test half-open slot race

	// Advance again past the second open period.
	fc.Advance(testtime.D50ms + time.Nanosecond)

	// Now concurrently race for the single half-open slot.
	// MaxRequests=1 means only one Allow() call should return true.
	// We do not call done() inside goroutines to avoid the allowed→closed
	// transition during the race (done(nil) would close the circuit and let
	// later goroutines through as closed).
	var (
		allowedCount atomic.Int32
		wg           sync.WaitGroup
	)

	// Barrier so all goroutines start at the same time.
	start := make(chan struct{})
	dones := make([]func(error), concurrency)
	var donesMu sync.Mutex

	for i := range concurrency {
		i := i
		wg.Go(func() {
			<-start
			ok, doneFn := a.Allow()
			if ok {
				allowedCount.Add(1)
				donesMu.Lock()
				dones[i] = doneFn
				donesMu.Unlock()
			}
		})
	}

	close(start) // release all goroutines simultaneously
	wg.Wait()

	// Call any captured done callbacks after counting.
	donesMu.Lock()
	for _, doneFn := range dones {
		if doneFn != nil {
			doneFn(nil)
		}
	}
	donesMu.Unlock()

	assert.Equal(t, int32(1), allowedCount.Load(),
		"MaxRequests=1 must allow exactly 1 concurrent request in half-open state")
}

// TestAdapter_Interval_ResetsCountsInClosedState verifies that when Interval>0,
// the closed-state counts are cleared after each interval period, resetting the
// consecutive failure counter so the breaker does not trip.
func TestAdapter_Interval_ResetsCountsInClosedState(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:     "test-interval",
		Interval: testtime.D100ms,
		ReadyToTrip: func(c Counts) bool {
			return c.ConsecutiveFailures > 5
		},
	})

	// 5 failures — not enough to trip (threshold > 5).
	for range 5 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	// Advance past the interval — counts should reset.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	// Trigger a new request to force the generation reset via currentState.
	_, done := a.Allow()
	done(nil) // success — breaker should still be closed

	assert.Equal(t, StateClosed, a.State(), "breaker must remain closed after interval reset")

	// 5 more failures after reset — still should not trip (fresh counts).
	for range 5 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	assert.Equal(t, StateClosed, a.State(), "breaker must remain closed: counts reset by interval")
}

// TestAdapter_CrossGeneration_DoneIgnored verifies that a done callback from a
// prior generation does not corrupt the counts of the current generation.
func TestAdapter_CrossGeneration_DoneIgnored(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:    "test-cross-gen",
		Timeout: testtime.D100ms,
	})

	// Acquire a done callback from generation 0.
	_, oldDone := a.Allow()

	// Trip the breaker (6 failures → open, bumps generation).
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())

	// Advance to half-open (new generation).
	fc.Advance(testtime.D100ms + time.Nanosecond)
	require.Equal(t, StateHalfOpen, a.State())

	// Call the old done callback — must be ignored (cross-generation).
	oldDone(nil)

	// State and counts must be unaffected by the stale callback.
	assert.Equal(t, StateHalfOpen, a.State(), "old done must not affect current generation state")
}

// TestAdapter_HalfOpen_PartialSuccessThenFailure verifies that if half-open
// probes partially succeed but then fail before MaxRequests successes, the
// breaker returns to open state.
func TestAdapter_HalfOpen_PartialSuccessThenFailure(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:        "test-halfopen-partial",
		MaxRequests: 3,
		Timeout:     testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())

	// Advance to half-open.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	// 2 successes, then 1 failure — should reopen.
	_, done1 := a.Allow()
	done1(nil)
	_, done2 := a.Allow()
	done2(nil)
	_, done3 := a.Allow()
	done3(errors.New("failure"))

	assert.Equal(t, StateOpen, a.State(), "partial success then failure must reopen the circuit")
}

// TestAdapter_IsSuccessful_CustomClassifier verifies that a custom IsSuccessful
// function can classify a specific sentinel error as success.
func TestAdapter_IsSuccessful_CustomClassifier(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("expected-not-fatal")
	a := mustNew(t, Config{
		Name: "test-is-successful",
		IsSuccessful: func(err error) bool {
			return errors.Is(err, sentinel) || err == nil
		},
	})

	// Send 6 requests with sentinel error — should be classified as success.
	for range 6 {
		_, done := a.Allow()
		done(sentinel) // counts as success
	}

	// Breaker should remain closed (no failures recorded).
	assert.Equal(t, StateClosed, a.State(),
		"custom IsSuccessful must classify sentinel error as success, not failure")
}

// TestAdapter_OnStateChange_PanicDoesNotStrandHalfOpenSlot verifies that a
// panicking OnStateChange callback fired during the open→half-open transition
// does not strand the probe slot. The panic propagates out of Allow() (we do
// NOT recover — that would mask the user bug); but because the two-pass
// beforeRequest fires the callback BEFORE counts.Requests++, a follow-up
// Allow() call succeeds cleanly because the slot was never consumed.
//
// Regression: PR#385 review FIND (post-FIND-001 reordering placed the
// callback after counts.Requests++, permanently stranding the slot).
func TestAdapter_OnStateChange_PanicDoesNotStrandHalfOpenSlot(t *testing.T) {
	t.Parallel()

	var fireCount atomic.Int32
	a, fc := mustNewWithClock(t, Config{
		Name:        "panic-strand",
		MaxRequests: 1,
		Timeout:     testtime.D100ms,
		OnStateChange: func(name string, from, to State) {
			if from == StateOpen && to == StateHalfOpen {
				fireCount.Add(1)
				panic("test: simulated callback panic")
			}
		},
	})

	// Trip the breaker (closed → open).
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())

	// Advance past Timeout so the next Allow triggers open → half-open
	// and fires the panicking callback. The panic propagates out of Allow.
	fc.Advance(testtime.D100ms + smallDelta)

	require.Panics(t, func() {
		_, _ = a.Allow()
	}, "callback panic must propagate out of Allow")
	require.Equal(t, int32(1), fireCount.Load(), "callback fired exactly once")

	// Critical assertion: the slot was NOT consumed despite the panic, so
	// the next Allow can claim the half-open probe and close the breaker.
	allowed, done := a.Allow()
	require.True(t, allowed, "follow-up Allow must succeed (slot not stranded)")
	require.NotNil(t, done)
	done(nil)
	require.Equal(t, StateClosed, a.State(), "successful probe must close the breaker")
}

// TestAdapter_OnStateChange_CanCallAllowReentrant verifies that the
// OnStateChange callback may safely call Adapter methods (Allow, State)
// from within itself — the two-pass beforeRequest fires callbacks outside
// the breaker mutex, so reentrant calls do not deadlock.
//
// Without the lock-out invariant this test would deadlock and the test
// runner would terminate it via timeout (visible in CI as a failure).
func TestAdapter_OnStateChange_CanCallAllowReentrant(t *testing.T) {
	t.Parallel()

	var observedFromCallback State
	var observed atomic.Bool
	var a *Adapter
	cfg := Config{
		Name:        "reentrant",
		MaxRequests: 1,
		Timeout:     testtime.D100ms,
		OnStateChange: func(name string, from, to State) {
			if from == StateOpen && to == StateHalfOpen {
				// Reentrant call — must not deadlock.
				observedFromCallback = a.State()
				observed.Store(true)
			}
		},
	}
	var fc *clockmock.FakeClock
	a, fc = mustNewWithClock(t, cfg)

	// Trip then advance past Timeout to trigger open→half-open.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())
	fc.Advance(testtime.D100ms + smallDelta)

	// Run Allow in a goroutine with a generous timeout so a deadlock
	// surfaces as a test failure rather than hanging the suite.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_, _ = a.Allow()
	}()

	timer := time.NewTimer(testtime.D2s)
	defer timer.Stop()
	select {
	case <-doneCh:
	case <-timer.C:
		t.Fatal("Allow deadlocked — reentrant State() inside callback blocked")
	}

	require.True(t, observed.Load(), "callback must have observed state reentrantly")
	require.Equal(t, StateHalfOpen, observedFromCallback,
		"reentrant State() inside the open→half-open callback should observe StateHalfOpen")
}

// TestAdapter_OnStateChange_NotCalledConcurrently verifies that the
// closed→open transition is recorded exactly once even under concurrent load.
func TestAdapter_OnStateChange_NotCalledConcurrently(t *testing.T) {
	t.Parallel()
	var transitionCount atomic.Int32
	a := mustNew(t, Config{
		Name: "test-oncall-concurrent",
		OnStateChange: func(_ string, from, to State) {
			if from == StateClosed && to == StateOpen {
				transitionCount.Add(1)
			}
		},
	})

	// 8 goroutines each try to trip the breaker concurrently.
	var wg sync.WaitGroup
	start := make(chan struct{})

	// Pre-load 5 failures (one short of tripping) to set the stage.
	for range 5 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	for range 8 {
		wg.Go(func() {
			<-start
			_, done := a.Allow()
			if done != nil {
				done(errors.New("failure"))
			}
		})
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), transitionCount.Load(),
		"closed→open transition must fire exactly once despite concurrent failures")
}

// TestAdapter_HalfOpen_MaxRequestsZeroDefaultsToOne verifies that MaxRequests=0
// is treated as 1, preventing the uint32 underflow/zero comparison edge case.
func TestAdapter_HalfOpen_MaxRequestsZeroDefaultsToOne(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:        "test-maxreq-zero",
		MaxRequests: 0, // should default to 1
		Timeout:     testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())

	// Advance to half-open.
	fc.Advance(testtime.D100ms + time.Nanosecond)

	// Only 1 request should be allowed in half-open (MaxRequests defaulted to 1).
	allowed1, done1 := a.Allow()
	require.True(t, allowed1, "first request must be allowed in half-open")

	allowed2, _ := a.Allow()
	assert.False(t, allowed2, "second request must be rejected — MaxRequests=0 defaults to 1")

	done1(nil) // complete the first request
}

// TestAdapter_OpenState_RejectsAllUntilTimeout verifies that all requests are
// rejected while the breaker is open, and only the first request after the
// timeout triggers the half-open probe.
func TestAdapter_OpenState_RejectsAllUntilTimeout(t *testing.T) {
	t.Parallel()
	a, fc := mustNewWithClock(t, Config{
		Name:    "test-open-rejects",
		Timeout: testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	require.Equal(t, StateOpen, a.State())

	// All 10 requests should be rejected before timeout.
	for range 10 {
		allowed, done := a.Allow()
		assert.False(t, allowed, "all requests must be rejected in open state")
		assert.Nil(t, done)
	}

	// Advance clock to just before timeout — still open.
	fc.Advance(testtime.D100ms - time.Nanosecond)
	allowed, _ := a.Allow()
	assert.False(t, allowed, "request must still be rejected just before timeout")

	// Advance smallDelta past timeout — now half-open probe should be allowed.
	fc.Advance(smallDelta)
	allowed, done := a.Allow()
	assert.True(t, allowed, "first request after timeout must be allowed (half-open probe)")
	if done != nil {
		done(nil)
	}
}
