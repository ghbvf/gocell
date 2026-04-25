package distlock

// Option is a functional option for configuring a Locker.
type Option func(*config)

// config holds all tunable parameters for a Locker.
type config struct {
	// renewFraction controls when the manager schedules the next renewal
	// relative to the TTL. Default 0.5 means renew at ttl/2.
	// ref: go-redsync/redsync — redsync uses 2/3 factor; we default to 1/2
	// for a wider safety margin.
	renewFraction float64

	// driftFactor accounts for clock skew between the local process and the
	// backend. The effective expiry is ttl * (1 - driftFactor). Default 0.01.
	// ref: go-redsync/redsync redsync.go driftFactor=0.01 — adopted as-is
	driftFactor float64

	// clock is the time source. Defaults to realClock{}.
	clock Clock
}

func defaultConfig() config {
	return config{
		renewFraction: 0.5,
		driftFactor:   0.01,
		clock:         realClock{},
	}
}

// WithRenewFraction sets the fraction of TTL at which the shared manager
// schedules the next renewal. Must be in (0, 1). Default: 0.5.
func WithRenewFraction(f float64) Option {
	return func(c *config) {
		c.renewFraction = f
	}
}

// WithDriftFactor sets the clock-drift tolerance factor. The manager subtracts
// ttl*driftFactor from the renewal deadline to guard against skew-induced
// expiry. Must be in [0, 1). Default: 0.01.
//
// ref: go-redsync/redsync redsync.go driftFactor=0.01
func WithDriftFactor(f float64) Option {
	return func(c *config) {
		c.driftFactor = f
	}
}

// WithClock replaces the default real-time clock with a controllable
// implementation. Intended for testing only.
func WithClock(clk Clock) Option {
	return func(c *config) {
		c.clock = clk
	}
}
