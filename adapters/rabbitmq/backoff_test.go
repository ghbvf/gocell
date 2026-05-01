package rabbitmq

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const (
	backoffD4s    = 4 * time.Second
	backoffD400ms = 400 * time.Millisecond
	backoffD800ms = 800 * time.Millisecond
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
		{name: "attempt 0 returns base", base: time.Second, maxDelay: testtime.D30s, attempt: 0, want: time.Second},
		{name: "attempt 1 doubles", base: time.Second, maxDelay: testtime.D30s, attempt: 1, want: testtime.D2s},
		{name: "attempt 2 quadruples", base: time.Second, maxDelay: testtime.D30s, attempt: 2, want: backoffD4s},
		{name: "attempt 10 exceeds max", base: time.Second, maxDelay: testtime.D30s, attempt: 10, want: testtime.D30s},
		{name: "attempt 34 overflow guard", base: time.Second, maxDelay: testtime.D30s, attempt: 34, want: testtime.D30s},
		{name: "attempt 100 far overflow", base: time.Second, maxDelay: testtime.D30s, attempt: 100, want: testtime.D30s},

		// Edge cases.
		{name: "base=0 returns 0", base: 0, maxDelay: testtime.D30s, attempt: 5, want: 0},
		{name: "negative base returns 0", base: -time.Second, maxDelay: testtime.D30s, attempt: 3, want: 0},
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
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, testtime.D100ms},
		{1, testtime.D200ms},
		{2, backoffD400ms},
		{3, backoffD800ms},
	}
	for _, c := range cases {
		got := outbox.ExponentialDelay(testtime.D100ms, testtime.D10s, c.attempt)
		assert.Equal(t, c.want, got, "attempt=%d", c.attempt)
	}
}
