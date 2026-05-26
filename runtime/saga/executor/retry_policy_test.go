package executor

import (
	"math/rand/v2"
	"testing"
	"time"

	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// deterministicJitter is a fixed-seed jitter source for deterministic tests.
type deterministicJitter struct {
	r *rand.Rand
}

func newDeterministicJitter(seed1, seed2 uint64) *deterministicJitter {
	return &deterministicJitter{r: rand.New(rand.NewPCG(seed1, seed2))} //nolint:gosec // deterministic test jitter
}

func (d *deterministicJitter) Int64N(n int64) int64 {
	return d.r.Int64N(n)
}

// TestResolvePolicy verifies inheritance priority: step > def > default.
func TestResolvePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		step         ksaga.RetryPolicy
		def          ksaga.RetryPolicy
		wantAttempts int
		wantBase     time.Duration
		wantMax      time.Duration
	}{
		{
			name:         "all_zero_uses_defaults",
			step:         ksaga.RetryPolicy{},
			def:          ksaga.RetryPolicy{},
			wantAttempts: DefaultMaxAttempts,
			wantBase:     DefaultBaseInterval,
			wantMax:      DefaultMaxInterval,
		},
		{
			name:         "def_overrides_defaults",
			step:         ksaga.RetryPolicy{},
			def:          ksaga.RetryPolicy{MaxAttempts: 5, BaseInterval: 200 * time.Millisecond, MaxInterval: 10 * time.Second},
			wantAttempts: 5,
			wantBase:     200 * time.Millisecond,
			wantMax:      10 * time.Second,
		},
		{
			name:         "step_overrides_def",
			step:         ksaga.RetryPolicy{MaxAttempts: 3, BaseInterval: 50 * time.Millisecond, MaxInterval: 5 * time.Second},
			def:          ksaga.RetryPolicy{MaxAttempts: 5, BaseInterval: 200 * time.Millisecond, MaxInterval: 10 * time.Second},
			wantAttempts: 3,
			wantBase:     50 * time.Millisecond,
			wantMax:      5 * time.Second,
		},
		{
			name:         "step_partial_overrides_def_partial",
			step:         ksaga.RetryPolicy{MaxAttempts: 7},
			def:          ksaga.RetryPolicy{BaseInterval: 300 * time.Millisecond, MaxInterval: 20 * time.Second},
			wantAttempts: 7,
			wantBase:     300 * time.Millisecond,
			wantMax:      20 * time.Second,
		},
		{
			name:         "step_partial_overrides_def_partial_base_only",
			step:         ksaga.RetryPolicy{BaseInterval: 400 * time.Millisecond},
			def:          ksaga.RetryPolicy{MaxAttempts: 4, MaxInterval: 15 * time.Second},
			wantAttempts: 4,
			wantBase:     400 * time.Millisecond,
			wantMax:      15 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := resolvePolicy(tc.step, tc.def)
			if p.maxAttempts != tc.wantAttempts {
				t.Errorf("maxAttempts = %d, want %d", p.maxAttempts, tc.wantAttempts)
			}
			if p.base != tc.wantBase {
				t.Errorf("base = %v, want %v", p.base, tc.wantBase)
			}
			if p.max != tc.wantMax {
				t.Errorf("max = %v, want %v", p.max, tc.wantMax)
			}
		})
	}
}

// TestShouldRetry verifies boundary conditions.
func TestShouldRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		maxAttempts int
		attempt     int
		want        bool
	}{
		{"max1_attempt1_no_retry", 1, 1, false},
		{"max1_attempt0_would_retry_but_attempt0_means_first", 1, 0, true},
		{"max3_attempt1_yes", 3, 1, true},
		{"max3_attempt2_yes", 3, 2, true},
		{"max3_attempt3_no", 3, 3, false},
		{"max3_attempt4_no", 3, 4, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := resolvedPolicy{maxAttempts: tc.maxAttempts, base: 100 * time.Millisecond, max: 30 * time.Second}
			got := p.ShouldRetry(tc.attempt)
			if got != tc.want {
				t.Errorf("ShouldRetry(%d) with maxAttempts=%d = %v, want %v",
					tc.attempt, tc.maxAttempts, got, tc.want)
			}
		})
	}
}

// TestBackoff_Capped verifies that Backoff respects MaxInterval cap.
func TestBackoff_Capped(t *testing.T) {
	t.Parallel()
	j := newDeterministicJitter(42, 1337)
	p := resolvedPolicy{maxAttempts: 10, base: 100 * time.Millisecond, max: 500 * time.Millisecond}

	// attempt=20 should far exceed the cap; result should be capped at max (with jitter)
	d := p.Backoff(20, j)
	if d > p.max {
		t.Errorf("Backoff(20) = %v, exceeds max %v", d, p.max)
	}
}

// TestBackoff_JitterRange verifies jitter falls in [0.8d, d].
func TestBackoff_JitterRange(t *testing.T) {
	t.Parallel()
	// Use a fresh jitter source for each sub-test to avoid coupling
	for attempt := 0; attempt < 5; attempt++ {
		j := newDeterministicJitter(uint64(attempt+1), 999)
		p := resolvedPolicy{maxAttempts: 10, base: 100 * time.Millisecond, max: 30 * time.Second}
		d := p.Backoff(attempt, j)
		raw := koutbox.ExponentialDelay(p.base, p.max, attempt)
		lower := raw - raw/5
		if raw == 0 {
			if d != 0 {
				t.Errorf("attempt=%d: Backoff = %v, want 0 for zero raw", attempt, d)
			}
			continue
		}
		if d < lower {
			t.Errorf("attempt=%d: Backoff = %v < lower bound %v", attempt, d, lower)
		}
		if d > raw {
			t.Errorf("attempt=%d: Backoff = %v > upper bound %v", attempt, d, raw)
		}
	}
}

// TestBackoff_Deterministic verifies same seed produces same result.
func TestBackoff_Deterministic(t *testing.T) {
	t.Parallel()
	j1 := newDeterministicJitter(12345, 67890)
	j2 := newDeterministicJitter(12345, 67890)
	p := resolvedPolicy{maxAttempts: 5, base: 100 * time.Millisecond, max: 30 * time.Second}

	for attempt := 0; attempt < 5; attempt++ {
		d1 := p.Backoff(attempt, j1)
		d2 := p.Backoff(attempt, j2)
		if d1 != d2 {
			t.Errorf("attempt=%d: non-deterministic: %v vs %v", attempt, d1, d2)
		}
	}
}

// TestBackoff_ZeroPolicy verifies zero policy (1 attempt, zero base) returns 0.
func TestBackoff_ZeroPolicy(t *testing.T) {
	t.Parallel()
	j := newDeterministicJitter(1, 2)
	p := resolvedPolicy{maxAttempts: 1, base: 0, max: 0}
	d := p.Backoff(0, j)
	if d != 0 {
		t.Errorf("zero policy Backoff = %v, want 0", d)
	}
}
