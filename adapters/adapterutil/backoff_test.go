package adapterutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/adapters/adapterutil"
)

// TestExponentialBackoffWithJitter_Growth verifies that the returned delay
// grows with attempt number and that jitter stays within the ±25% envelope.
func TestExponentialBackoffWithJitter_Growth(t *testing.T) {
	base := time.Second
	max := 30 * time.Second

	tests := []struct {
		name    string
		attempt int
		minD    time.Duration
		maxD    time.Duration
	}{
		// attempt 0: base * 2^0 = 1s, jitter [0.75s, 1.25s]
		{name: "attempt_0", attempt: 0, minD: 750 * time.Millisecond, maxD: 1250 * time.Millisecond},
		// attempt 1: base * 2^1 = 2s, jitter [1.5s, 2.5s]
		{name: "attempt_1", attempt: 1, minD: 1500 * time.Millisecond, maxD: 2500 * time.Millisecond},
		// attempt 2: base * 2^2 = 4s, jitter [3s, 5s]
		{name: "attempt_2", attempt: 2, minD: 3 * time.Second, maxD: 5 * time.Second},
		// Capped region: jitter on max → [0.75*30s, 30s]
		{name: "attempt_10_capped", attempt: 10, minD: 22500 * time.Millisecond, maxD: 30 * time.Second},
		{name: "attempt_34_overflow", attempt: 34, minD: 22500 * time.Millisecond, maxD: 30 * time.Second},
		{name: "attempt_100_far", attempt: 100, minD: 22500 * time.Millisecond, maxD: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 100 {
				got := adapterutil.ExponentialBackoffWithJitter(base, max, tt.attempt)
				assert.GreaterOrEqual(t, got, tt.minD,
					"attempt %d: delay %v < min %v", tt.attempt, got, tt.minD)
				assert.LessOrEqual(t, got, tt.maxD,
					"attempt %d: delay %v > max %v", tt.attempt, got, tt.maxD)
			}
		})
	}
}

// TestExponentialBackoffWithJitter_NeverExceedMax verifies the hard cap even
// for large attempt values.
func TestExponentialBackoffWithJitter_NeverExceedMax(t *testing.T) {
	base := time.Second
	max := 30 * time.Second

	for attempt := range 150 {
		for range 50 {
			got := adapterutil.ExponentialBackoffWithJitter(base, max, attempt)
			assert.LessOrEqual(t, got, max,
				"attempt %d: delay %v exceeds max %v", attempt, got, max)
		}
	}
}

// TestDownJitter verifies 0-25% downward jitter stays in [0.75*d, d].
func TestDownJitter(t *testing.T) {
	t.Run("zero_returns_zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), adapterutil.DownJitter(0))
	})
	t.Run("negative_returns_zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), adapterutil.DownJitter(-1*time.Second))
	})
	t.Run("positive_in_range", func(t *testing.T) {
		d := 30 * time.Second
		for range 100 {
			got := adapterutil.DownJitter(d)
			assert.GreaterOrEqual(t, got, time.Duration(float64(d)*0.75),
				"got %v < 0.75*%v", got, d)
			assert.LessOrEqual(t, got, d,
				"got %v > %v", got, d)
		}
	})
}

// TestExponentialBackoffWithJitter_SmallBase verifies behavior with a very
// small base that doesn't prematurely reach the cap.
func TestExponentialBackoffWithJitter_SmallBase(t *testing.T) {
	base := time.Millisecond
	max := time.Hour

	// attempt 10: 1ms * 2^10 = 1.024s — well below 1h
	for range 50 {
		got := adapterutil.ExponentialBackoffWithJitter(base, max, 10)
		assert.GreaterOrEqual(t, got, 750*time.Millisecond)
		assert.LessOrEqual(t, got, 1300*time.Millisecond)
	}
	// attempt 30: still below 1h
	for range 50 {
		got := adapterutil.ExponentialBackoffWithJitter(base, max, 30)
		assert.Less(t, got, max, "attempt 30 with 1ms base should not hit 1h cap")
	}
}
