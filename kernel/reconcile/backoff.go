package reconcile

import (
	"math"
	"sync"
	"time"
)

// defaultBackoffBase is the base delay mirroring client-go
// ItemExponentialFailureRateLimiter (5ms).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
const defaultBackoffBase = 5 * time.Millisecond

// defaultBackoffMax is the maximum delay mirroring client-go
// ItemExponentialFailureRateLimiter (1000s).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
const defaultBackoffMax = 1000 * time.Second

// entityBackoff tracks per-entity failure counts and computes an exponential
// backoff delay for each entity. It mirrors client-go's
// ItemExponentialFailureRateLimiter: base·2^n, capped at max, no jitter.
// The first When call for an entity returns base (n=0 before increment).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
type entityBackoff struct {
	mu       sync.Mutex
	failures map[string]int
	base     time.Duration
	max      time.Duration
}

// newEntityBackoff creates an entityBackoff with the given parameters.
// base <= 0 defaults to 5ms; max <= 0 defaults to 1000s.
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
func newEntityBackoff(base, max time.Duration) *entityBackoff {
	if base <= 0 {
		base = defaultBackoffBase
	}
	if max <= 0 {
		max = defaultBackoffMax
	}
	return &entityBackoff{
		failures: make(map[string]int),
		base:     base,
		max:      max,
	}
}

// When returns the backoff delay for entityID and increments its failure count.
// The first call returns base; subsequent calls double each time, capped at max.
// The result is always in [base, max] — never negative or zero.
func (b *entityBackoff) When(entityID string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := b.failures[entityID]
	b.failures[entityID] = n + 1

	// Compute base · 2^n using float64 to avoid integer overflow.
	ns := float64(b.base.Nanoseconds()) * math.Pow(2, float64(n))
	if ns > math.MaxInt64 || time.Duration(ns) > b.max {
		return b.max
	}
	return time.Duration(ns)
}

// Forget clears the failure count for entityID so the next When call
// returns base again. It is a no-op for unknown entities.
func (b *entityBackoff) Forget(entityID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, entityID)
}
