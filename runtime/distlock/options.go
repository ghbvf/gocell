package distlock

import "time"

const (
	// defaultDistLockReleaseTimeout is the context deadline applied to background
	// Driver.Release calls to prevent indefinite hangs.
	defaultDistLockReleaseTimeout = 5 * time.Second
)

// Option is a functional option for configuring a Locker.
type Option func(*config)

// config holds all tunable parameters for a Locker.
type config struct {
	// renewFraction controls when the manager schedules the next renewal
	// relative to the TTL. Default 0.5 means renew at ttl/2.
	// ref: go-redsync/redsync — redsync uses 2/3 factor; we default to 1/2
	// for a wider safety margin.
	renewFraction float64

	// driftFactor sets the renewal I/O timeout safety margin. See WithDriftFactor.
	driftFactor float64

	// releaseTimeout is the context deadline applied to background Driver.Release
	// calls. Default 5s. See WithReleaseTimeout.
	releaseTimeout time.Duration

	// maxRenewAttempts is the number of Driver.Renew attempts per renewal tick
	// before the lock is declared lost. Only transient I/O errors are retried;
	// ownership-lost (held=false) is permanent and skips retries. Default 3.
	// See WithMaxRenewAttempts.
	maxRenewAttempts int

	// clock is the time source. Defaults to realClock{}.
	clock Clock
}

func defaultConfig() config {
	return config{
		renewFraction:    0.5,
		driftFactor:      0.01,
		releaseTimeout:   defaultDistLockReleaseTimeout,
		maxRenewAttempts: 3,
		clock:            realClock{},
	}
}

// WithRenewFraction sets the fraction of TTL at which the shared manager
// schedules the next renewal. Must be in (0, 1). Default: 0.5.
func WithRenewFraction(f float64) Option {
	return func(c *config) {
		c.renewFraction = f
	}
}

// WithDriftFactor sets the renewal I/O timeout safety margin. The Renew RPC
// context deadline is set to clock.Now() + ttl × (1 − driftFactor), so the
// manager gives up on a slow Driver.Renew before the backend key would expire.
// Does NOT alter the TTL written to the backend.
//
// Recommended range: 0.01–0.05. Higher values make Renew calls fail more often
// under transient I/O slowness; lower values risk the call outliving the backend
// TTL on slow networks. Must be in [0, 1). Default: 0.01.
//
// ref: go-redsync/redsync redsync.go driftFactor=0.01
func WithDriftFactor(f float64) Option {
	return func(c *config) {
		c.driftFactor = f
	}
}

// WithReleaseTimeout sets the context deadline applied to each background
// Driver.Release call issued by the fire-and-forget release path. If Redis (or
// another backend) hangs, the Release goroutine will be unblocked after this
// duration rather than leaking indefinitely.
//
// Default: 5s (conservative; tune down for low-latency backends or up for
// high-latency ones). Must be > 0; New() panics if the final value is ≤ 0.
func WithReleaseTimeout(d time.Duration) Option {
	return func(c *config) {
		c.releaseTimeout = d
	}
}

// WithMaxRenewAttempts sets the maximum number of Driver.Renew attempts per
// renewal tick before the lock is declared lost. Must be ≥ 1. Default: 3.
//
// Only transient I/O errors (err != nil) are retried; permanent ownership-lost
// responses (held=false, err=nil) immediately declare the lock lost regardless
// of this setting.
//
// All retry attempts share the same renewTimeout window derived from the lock
// TTL and drift factor. New() panics if the final value is < 1.
func WithMaxRenewAttempts(n int) Option {
	return func(c *config) {
		c.maxRenewAttempts = n
	}
}

// WithClock replaces the default real-time clock with a controllable
// implementation. Intended for testing only.
func WithClock(clk Clock) Option {
	return func(c *config) {
		c.clock = clk
	}
}
