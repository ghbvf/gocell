package rabbitmq

import (
	"math/bits"
	"time"
)

const (
	// backoffMultiplierShift controls exponential growth: delay = base << (attempt * backoffMultiplierShift)
	// (i.e. multiplier = 2^backoffMultiplierShift = 2x per attempt).
	backoffMultiplierShift = 1
)

// safeDelay is an alias for exponentialDelay retained so existing rabbitmq
// tests can reference the helper without churn.
func safeDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	return exponentialDelay(base, maxDelay, attempt)
}

// exponentialDelay computes base * 2^attempt with overflow protection, capped at maxDelay.
// If base is zero or negative, it returns 0. The bits.Len64 technique determines
// the maximum safe left-shift to avoid integer overflow before comparing against maxDelay.
func exponentialDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	maxSafeShift := 63 - bits.Len64(uint64(base))
	if attempt > maxSafeShift {
		return maxDelay
	}
	delay := base * (1 << (uint(attempt) * backoffMultiplierShift))
	if delay <= 0 || delay > maxDelay {
		return maxDelay
	}
	return delay
}
