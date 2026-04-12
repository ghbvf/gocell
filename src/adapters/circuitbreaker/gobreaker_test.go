package circuitbreaker

import (
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/http/middleware"
)

// Compile-time check: Adapter implements CircuitBreakerPolicy.
var _ middleware.CircuitBreakerPolicy = (*Adapter)(nil)

func TestAdapter_DefaultConfig_Closed(t *testing.T) {
	a := New(Config{Name: "test-default"})
	done, err := a.Allow()
	require.NoError(t, err, "newly created breaker must be in closed state")
	require.NotNil(t, done)
	done(true) // report success
}

func TestAdapter_OpensAfterFailures(t *testing.T) {
	a := New(Config{
		Name: "test-open",
		// Default ReadyToTrip: consecutive failures > 5
	})

	// Trip the breaker with 6 consecutive failures.
	for i := 0; i < 6; i++ {
		done, err := a.Allow()
		require.NoError(t, err, "breaker should still be closed during failure reporting")
		done(false)
	}

	// Now the circuit should be open.
	done, err := a.Allow()
	assert.Error(t, err, "breaker must be open after consecutive failures")
	assert.Nil(t, done)
}

func TestAdapter_HalfOpenAfterTimeout(t *testing.T) {
	a := New(Config{
		Name:    "test-halfopen",
		Timeout: 100 * time.Millisecond, // short timeout for test
	})

	// Trip the breaker.
	for i := 0; i < 6; i++ {
		done, _ := a.Allow()
		done(false)
	}
	_, err := a.Allow()
	require.Error(t, err, "breaker must be open")

	// Poll until the breaker transitions to half-open (generous timeout for slow CI).
	require.Eventually(t, func() bool {
		done, err := a.Allow()
		if err != nil {
			return false
		}
		done(true) // successful probe
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
		done, _ := a.Allow()
		done(false)
	}

	// Poll until half-open, then send successful probe.
	require.Eventually(t, func() bool {
		done, err := a.Allow()
		if err != nil {
			return false
		}
		done(true) // successful probe → close
		return true
	}, 2*time.Second, 25*time.Millisecond, "breaker must reach half-open")

	// Should be back to closed — multiple requests allowed.
	for i := 0; i < 3; i++ {
		done, err := a.Allow()
		assert.NoError(t, err, "breaker must be closed after half-open success (attempt %d)", i)
		if done != nil {
			done(true)
		}
	}
}

func TestAdapter_OnStateChangeCallback(t *testing.T) {
	var transitions []string
	a := New(Config{
		Name:    "test-callback",
		Timeout: 100 * time.Millisecond,
		OnStateChange: func(name string, from, to gobreaker.State) {
			transitions = append(transitions, from.String()+"→"+to.String())
		},
	})

	// Trip: closed → open
	for i := 0; i < 6; i++ {
		done, _ := a.Allow()
		done(false)
	}
	require.Contains(t, transitions, "closed→open")

	// Poll until half-open → closed transition completes.
	require.Eventually(t, func() bool {
		done, err := a.Allow()
		if err != nil {
			return false
		}
		done(true) // successful probe → half-open → closed
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
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Trip after just 2 failures.
			return counts.ConsecutiveFailures > 2
		},
	})

	// 3 failures should trip.
	for i := 0; i < 3; i++ {
		done, _ := a.Allow()
		done(false)
	}

	_, err := a.Allow()
	assert.Error(t, err, "custom ReadyToTrip(>2) must trip after 3 failures")
}
