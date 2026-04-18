package rabbitmq

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/outbox"
)

func TestExponentialDelay(t *testing.T) {
	tests := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		attempt  int
		want     time.Duration
	}{
		// Normal progression: base=1s, max=30s.
		{name: "attempt 0 returns base", base: time.Second, maxDelay: 30 * time.Second, attempt: 0, want: time.Second},
		{name: "attempt 1 doubles", base: time.Second, maxDelay: 30 * time.Second, attempt: 1, want: 2 * time.Second},
		{name: "attempt 2 quadruples", base: time.Second, maxDelay: 30 * time.Second, attempt: 2, want: 4 * time.Second},
		{name: "attempt 10 exceeds max", base: time.Second, maxDelay: 30 * time.Second, attempt: 10, want: 30 * time.Second},
		{name: "attempt 34 overflow guard", base: time.Second, maxDelay: 30 * time.Second, attempt: 34, want: 30 * time.Second},
		{name: "attempt 100 far overflow", base: time.Second, maxDelay: 30 * time.Second, attempt: 100, want: 30 * time.Second},

		// Edge cases.
		{name: "base=0 returns 0", base: 0, maxDelay: 30 * time.Second, attempt: 5, want: 0},
		{name: "negative base returns 0", base: -time.Second, maxDelay: 30 * time.Second, attempt: 3, want: 0},
		{name: "large attempt returns maxDelay", base: time.Second, maxDelay: time.Minute, attempt: 200, want: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outbox.ExponentialDelay(tt.base, tt.maxDelay, tt.attempt)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestExponentialDelay_DoublesEachAttempt verifies that ExponentialDelay
// produces exactly 2x growth per attempt: base * 2^attempt.
func TestExponentialDelay_DoublesEachAttempt(t *testing.T) {
	const base = 100 * time.Millisecond
	const maxDelay = 10 * time.Second

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
	}
	for _, c := range cases {
		got := outbox.ExponentialDelay(base, maxDelay, c.attempt)
		assert.Equal(t, c.want, got, "attempt=%d", c.attempt)
	}
}
