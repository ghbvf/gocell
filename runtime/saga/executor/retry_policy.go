package executor

import (
	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used // non-crypto saga backoff jitter; injected seeded source, gosec G404 silenced at usage
	"math/rand/v2"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	koutbox "github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// Package-level defaults for resolved retry policy.
// These constants are the fallback values used by resolvePolicy when neither
// the step nor the definition sets a field (all zeros ⇒ inherit).
//
// NOTE: DefaultMaxAttempts of 1 means a single attempt with no retry.
// There is no "unlimited" option — retries are bounded, then compensation runs.
const (
	// DefaultMaxAttempts is the total number of Run invocations (including the
	// first) used when neither the step nor the definition specifies MaxAttempts.
	DefaultMaxAttempts = 1
	// DefaultBaseInterval is the first retry backoff base when neither the step
	// nor the definition specifies BaseInterval.
	DefaultBaseInterval = 100 * time.Millisecond
	// DefaultMaxInterval is the backoff cap when neither the step nor the
	// definition specifies MaxInterval.
	DefaultMaxInterval = 30 * time.Second
)

// jitterDivisor sets the bounded-jitter window width: the jittered delay lies in
// [d - d/jitterDivisor, d] where d = ExponentialDelay. A divisor of 5 yields a
// Temporal-style [0.8d, d] spread, avoiding the thundering-herd of full-jitter
// [0, d) while still decorrelating retriers. Referenced by Backoff and by the
// retry_policy_test jitter-range assertion (single source of the window width).
const jitterDivisor = 5

// jitterSource is a source of bounded random integers used for retry backoff.
// Declared here so executor.go and options.go can reference the same type;
// tests inject a deterministic implementation.
type jitterSource interface {
	// Int64N returns a non-negative random int64 in [0, n).
	// Panics if n <= 0 (same contract as rand.Rand.Int64N).
	Int64N(n int64) int64
}

// newDefaultJitter returns a production jitter source seeded from clk.
// math/rand/v2 is intentional: backoff jitter is not security-sensitive.
// Two decorrelated seeds are derived via splitmix64 to satisfy PCG's
// independence requirement (two consecutive time reads would be highly
// correlated and risk collisions in the period).
func newDefaultJitter(clk clock.Clock) jitterSource {
	seed1 := uint64(clk.Now().UnixNano())
	// splitmix64: derive a decorrelated second seed so PCG gets two
	// independent seeds (consecutive time reads are highly correlated).
	s := seed1 + 0x9e3779b97f4a7c15
	s = (s ^ (s >> 30)) * 0xbf58476d1ce4e5b9
	s = (s ^ (s >> 27)) * 0x94d049bb133111eb
	seed2 := s ^ (s >> 31)
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
		p.maxAttempts = DefaultMaxAttempts
	}

	if step.BaseInterval != 0 {
		p.base = step.BaseInterval
	} else if def.BaseInterval != 0 {
		p.base = def.BaseInterval
	} else {
		p.base = DefaultBaseInterval
	}

	if step.MaxInterval != 0 {
		p.max = step.MaxInterval
	} else if def.MaxInterval != 0 {
		p.max = def.MaxInterval
	} else {
		p.max = DefaultMaxInterval
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
// Formula: lower = d - d/jitterDivisor; result = lower + Int64N(d/jitterDivisor + 1).
// The +1 ensures Int64N never panics on a zero argument.
func (p resolvedPolicy) Backoff(attempt int, j jitterSource) time.Duration {
	d := koutbox.ExponentialDelay(p.base, p.max, attempt)
	if d <= 0 {
		return 0
	}
	lower := d - d/jitterDivisor
	return lower + time.Duration(j.Int64N(int64(d/jitterDivisor)+1))
}
