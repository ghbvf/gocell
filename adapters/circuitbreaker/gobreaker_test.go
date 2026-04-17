package circuitbreaker

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/http/middleware"
)

// Compile-time checks: Adapter implements Allower and CircuitBreakerRetryAfter.
var _ middleware.Allower = (*Adapter)(nil)
var _ middleware.CircuitBreakerRetryAfter = (*Adapter)(nil)

func TestAdapter_DefaultConfig_Closed(t *testing.T) {
	a := New(Config{Name: "test-default"})
	allowed, done := a.Allow()
	require.True(t, allowed, "newly created breaker must be in closed state")
	require.NotNil(t, done)
	done(nil) // report success
}

func TestAdapter_OpensAfterFailures(t *testing.T) {
	a := New(Config{
		Name: "test-open",
		// Default ReadyToTrip: consecutive failures > 5
	})

	// Trip the breaker with 6 consecutive failures.
	for i := 0; i < 6; i++ {
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
	a := New(Config{
		Name:    "test-halfopen",
		Timeout: 100 * time.Millisecond, // short timeout for test
	})

	// Trip the breaker.
	for i := 0; i < 6; i++ {
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
	}, 2*time.Second, 25*time.Millisecond, "breaker must transition to half-open after timeout")
}

func TestAdapter_ClosesAfterHalfOpenSuccess(t *testing.T) {
	a := New(Config{
		Name:    "test-close",
		Timeout: 100 * time.Millisecond,
	})

	// Trip the breaker.
	for i := 0; i < 6; i++ {
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
	}, 2*time.Second, 25*time.Millisecond, "breaker must reach half-open")

	// Should be back to closed — multiple requests allowed.
	for i := 0; i < 3; i++ {
		allowed, done := a.Allow()
		assert.True(t, allowed, "breaker must be closed after half-open success (attempt %d)", i)
		if done != nil {
			done(nil)
		}
	}
}

func TestAdapter_OnStateChangeCallback(t *testing.T) {
	var transitions []string
	a := New(Config{
		Name:    "test-callback",
		Timeout: 100 * time.Millisecond,
		OnStateChange: func(name string, from, to State) {
			transitions = append(transitions, from.String()+"→"+to.String())
		},
	})

	// Trip: closed → open
	for i := 0; i < 6; i++ {
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
	}, 2*time.Second, 25*time.Millisecond, "breaker must reach half-open for callback test")

	assert.Contains(t, transitions, "open→half-open")
	assert.Contains(t, transitions, "half-open→closed")
}

func TestAdapter_RetryAfter_CustomTimeout(t *testing.T) {
	a := New(Config{Name: "test-retry", Timeout: 30 * time.Second})
	assert.Equal(t, 30*time.Second, a.RetryAfter())
}

func TestAdapter_RetryAfter_DefaultTimeout(t *testing.T) {
	a := New(Config{Name: "test-retry-default"})
	assert.Equal(t, 60*time.Second, a.RetryAfter(), "default timeout is 60s")
}

func TestAdapter_CustomReadyToTrip(t *testing.T) {
	a := New(Config{
		Name: "test-custom-trip",
		ReadyToTrip: func(counts Counts) bool {
			// Trip after just 2 failures.
			return counts.ConsecutiveFailures > 2
		},
	})

	// 3 failures should trip.
	for i := 0; i < 3; i++ {
		_, done := a.Allow()
		done(errors.New("failure"))
	}

	allowed, _ := a.Allow()
	assert.False(t, allowed, "custom ReadyToTrip(>2) must trip after 3 failures")
}

func TestAdapter_State(t *testing.T) {
	a := New(Config{Name: "test-state"})
	assert.Equal(t, StateClosed, a.State(), "new breaker starts in closed state")
}

func TestAdapter_Allow_SuccessNilError(t *testing.T) {
	a := New(Config{Name: "test-success-nil"})
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
	a := New(Config{Name: "test-failure-err"})
	allowed, done := a.Allow()
	require.True(t, allowed)
	require.NotNil(t, done)

	// Calling done with an error must count as failure.
	done(errors.New("upstream error"))

	// One failure is not enough to trip (default threshold > 5), still closed.
	allowed2, _ := a.Allow()
	assert.True(t, allowed2, "single failure must not trip circuit with default settings")
}
