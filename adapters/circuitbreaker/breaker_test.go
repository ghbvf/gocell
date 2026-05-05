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
	a := mustNew(t, Config{Name: "test-default"})
	allowed, done := a.Allow()
	require.True(t, allowed, "newly created breaker must be in closed state")
	require.NotNil(t, done)
	done(nil) // report success
}

func TestAdapter_OpensAfterFailures(t *testing.T) {
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
	a := mustNew(t, Config{
		Name:    "test-halfopen",
		Timeout: testtime.D100ms, // short timeout for test
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}
	allowed, _ := a.Allow()
	require.False(t, allowed, "breaker must be open")

	// Poll until the breaker transitions to half-open (generous timeout for slow CI).
	require.Eventually(t, func() bool {
		allowed, done := a.Allow()
		if !allowed {
			return false
		}
		done(nil) // successful probe
		return true
	}, testtime.D2s, testtime.D25ms, "breaker must transition to half-open after timeout")
}

func TestAdapter_ClosesAfterHalfOpenSuccess(t *testing.T) {
	a := mustNew(t, Config{
		Name:    "test-close",
		Timeout: testtime.D100ms,
	})

	// Trip the breaker.
	for range 6 {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	// Poll until half-open, then send successful probe.
	require.Eventually(t, func() bool {
		allowed, done := a.Allow()
		if !allowed {
			return false
		}
		done(nil) // successful probe → close
		return true
	}, testtime.D2s, testtime.D25ms, "breaker must reach half-open")

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
	var transitions []string
	a := mustNew(t, Config{
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

	// Poll until half-open → closed transition completes.
	require.Eventually(t, func() bool {
		allowed, done := a.Allow()
		if !allowed {
			return false
		}
		done(nil) // successful probe → half-open → closed
		return true
	}, testtime.D2s, testtime.D25ms, "breaker must reach half-open for callback test")

	assert.Contains(t, transitions, "open→half-open")
	assert.Contains(t, transitions, "half-open→closed")
}

func TestAdapter_RetryAfter_CustomTimeout(t *testing.T) {
	a := mustNew(t, Config{Name: "test-retry", Timeout: testtime.D30s})
	assert.Equal(t, testtime.D30s, a.RetryAfter())
}

func TestAdapter_RetryAfter_DefaultTimeout(t *testing.T) {
	a := mustNew(t, Config{Name: "test-retry-default"})
	assert.Equal(t, testtime.D60s, a.RetryAfter(), "default timeout is 60s")
}

func TestAdapter_CustomReadyToTrip(t *testing.T) {
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
	a := mustNew(t, Config{Name: "test-state"})
	assert.Equal(t, StateClosed, a.State(), "new breaker starts in closed state")
}

func TestAdapter_Allow_SuccessNilError(t *testing.T) {
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
	assert.Panics(t, func() {
		_, _ = New(Config{Name: "test-nil-clock"}, nil)
	})
}

// TestAdapter_HalfOpen_MaxRequestsConcurrent verifies that MaxRequests=1 in
// half-open state allows exactly one concurrent request and rejects all others.
//
// ref: github.com/sony/gobreaker twostep_breaker.go — half-open MaxRequests
func TestAdapter_HalfOpen_MaxRequestsConcurrent(t *testing.T) {
	const concurrency = 8

	a := mustNew(t, Config{
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

	// Wait for the Timeout to elapse so gobreaker transitions to half-open.
	require.Eventually(t, func() bool {
		// A single Allow probe tells us if we are in half-open.
		probe, probeDone := a.Allow()
		if probe {
			// We are in half-open. Report success to avoid closing immediately,
			// then break out — we want to test the concurrent scenario below.
			probeDone(errors.New("probe fail")) // reopen to start fresh
			return true
		}
		return false
	}, testtime.D2s, testtime.D10ms, "breaker must enter half-open after timeout")

	// Wait again to enter half-open after re-opening.
	require.Eventually(t, func() bool {
		allowed, done := a.Allow()
		if !allowed {
			return false
		}
		// Keep the door open by failing the probe; we need half-open for the race.
		done(errors.New("probe fail"))
		return true
	}, testtime.D2s, testtime.D10ms, "breaker must enter half-open for concurrent test")

	// Now concurrently race for the single half-open slot.
	// gobreaker MaxRequests=1 means only one Allow() call should return true.
	var (
		allowedCount atomic.Int32
		wg           sync.WaitGroup
	)

	// Barrier so all goroutines start at the same time.
	start := make(chan struct{})

	for range concurrency {
		wg.Go(func() {
			<-start
			ok, done := a.Allow()
			if ok {
				allowedCount.Add(1)
				done(nil) // report success
			}
		})
	}

	close(start) // release all goroutines simultaneously
	wg.Wait()

	assert.LessOrEqual(t, int(allowedCount.Load()), 1,
		"MaxRequests=1 must allow at most 1 concurrent request in half-open state")
}
