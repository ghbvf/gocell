package adapterutil

import (
	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used // non-crypto reconnect jitter; gosec G404 already silenced at usage sites
	"math/rand/v2"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// ExponentialBackoffWithJitter returns the reconnect delay for attempt N using
// outbox.ExponentialDelay(base, max, attempt) with ±25% jitter. When the
// exponential value reaches/exceeds max, jitter is applied then capped so the
// result never exceeds max.
//
// When the exponential value is below max, ±25% jitter is applied and the
// result is capped at max if the positive jitter would overshoot.
//
// ref: adapters/rabbitmq/connection.go backoffDelay.
// ref: eclipse/autopaho auto.go ReconnectBackoff.
func ExponentialBackoffWithJitter(base, max time.Duration, attempt int) time.Duration {
	delay := outbox.ExponentialDelay(base, max, attempt)
	if delay >= max {
		return downJitter(max)
	}
	// Uncapped region: jitter on actual delay. Cap any overshoot from +25%.
	withJitter := addJitter(delay)
	if withJitter > max {
		return max
	}
	return withJitter
}

// DownJitter applies 0-25% downward jitter to d (used for stale-claim reclaim
// spreading). The result is in [0.75*d, d].
//
// ref: adapters/rabbitmq/connection.go addDownJitter.
func DownJitter(d time.Duration) time.Duration {
	return downJitter(d)
}

// downJitter is the internal implementation used by both DownJitter and
// ExponentialBackoffWithJitter.
func downJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// Remove up to 25% of d.
	reduction := rand.Int64N(int64(d)/4 + 1) //nolint:gosec // G404 R2-approved: reconnect down-jitter has no cryptographic requirement
	return d - time.Duration(reduction)
}

// addJitter applies ±25% random jitter to a duration.
// The result is in [0.75*d, 1.25*d].
func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// jitter range: 50% of d (from -25% to +25%)
	jitterRange := int64(d) / 2
	// offset: random value in [0, jitterRange]
	offset := rand.Int64N(jitterRange + 1) //nolint:gosec // G404 R2-approved: reconnect jitter has no cryptographic requirement
	// shift to [-25%, +25%]: subtract 25% of d
	return time.Duration(int64(d) - jitterRange/2 + offset)
}
