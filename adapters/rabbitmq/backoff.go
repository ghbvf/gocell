package rabbitmq

import (
	"math/bits"
	"time"
)

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
	delay := base * (1 << uint(attempt))
	if delay <= 0 || delay > maxDelay {
		return maxDelay
	}
	return delay
}
