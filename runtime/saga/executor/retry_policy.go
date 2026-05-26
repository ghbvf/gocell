package executor

import (
	"math/rand/v2"
	"time"

	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// Package-level defaults for resolved retry policy.
const (
	defaultMaxAttempts = 1
	pkgDefaultBase     = 100 * time.Millisecond
	pkgDefaultMax      = 30 * time.Second
)

// jitterSource is a source of bounded random integers used for retry backoff.
// Declared here so executor.go and options.go can reference the same type;
// tests inject a deterministic implementation.
type jitterSource interface {
	// Int64N returns a non-negative random int64 in [0, n).
	// Panics if n <= 0 (same contract as rand.Rand.Int64N).
	Int64N(n int64) int64
}

// newDefaultJitter returns a production jitter source seeded from the clock.
// math/rand/v2 is intentional: backoff jitter is not security-sensitive.
func newDefaultJitter() jitterSource {
	seed1 := uint64(time.Now().UnixNano())
	seed2 := uint64(time.Now().UnixNano() >> 17)
	return rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // non-security backoff jitter
}

// resolvedPolicy is the effective retry policy after inheritance resolution.
type resolvedPolicy struct {
	maxAttempts int
	base        time.Duration
	max         time.Duration
}

// resolvePolicy merges step-level, definition-level, and package defaults.
// Each field: non-zero step value wins; else non-zero def value; else package default.
func resolvePolicy(step, def ksaga.RetryPolicy) resolvedPolicy {
	p := resolvedPolicy{}

	if step.MaxAttempts != 0 {
		p.maxAttempts = step.MaxAttempts
	} else if def.MaxAttempts != 0 {
		p.maxAttempts = def.MaxAttempts
	} else {
		p.maxAttempts = defaultMaxAttempts
	}

	if step.BaseInterval != 0 {
		p.base = step.BaseInterval
	} else if def.BaseInterval != 0 {
		p.base = def.BaseInterval
	} else {
		p.base = pkgDefaultBase
	}

	if step.MaxInterval != 0 {
		p.max = step.MaxInterval
	} else if def.MaxInterval != 0 {
		p.max = def.MaxInterval
	} else {
		p.max = pkgDefaultMax
	}

	return p
}

// ShouldRetry reports whether another attempt is allowed.
// attempt is the number of attempts already made (1-based: first attempt = 1).
func (p resolvedPolicy) ShouldRetry(attempt int) bool {
	return attempt < p.maxAttempts
}

// Backoff computes the bounded-jitter delay before attempt `attempt` (0-based,
// so attempt=0 is before the first retry).
//
// Uses Temporal-style bounded jitter: [0.8d, d] where d = ExponentialDelay.
// This avoids the thundering-herd of full-jitter [0, d) while still spreading load.
//
// Formula: lower = d - d/5; result = lower + Int64N(d/5 + 1).
// The +1 ensures Int64N never panics on a zero argument.
func (p resolvedPolicy) Backoff(attempt int, j jitterSource) time.Duration {
	d := koutbox.ExponentialDelay(p.base, p.max, attempt)
	if d <= 0 {
		return 0
	}
	lower := d - d/5
	return lower + time.Duration(j.Int64N(int64(d/5)+1))
}
