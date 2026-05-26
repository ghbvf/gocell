package executor

import (
	"log/slog"
	"time"
)

// Option is a functional option for Executor construction.
type Option func(*Executor)

// WithLogger is a cumulative builder option. Category: builder-noop.
// Sets the structured logger; nil is silently ignored and the constructor
// default (slog.Default()) is kept. Safe to call multiple times; last
// non-nil value wins.
func WithLogger(l *slog.Logger) Option {
	return func(e *Executor) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithHeartbeatInterval is a direct-assign option. Category: measure-then-validate.
// Sets the interval between lease heartbeat calls. Zero or negative values
// are stored and rejected by NewExecutor's post-option validation
// (heartbeatInterval must be > 0 and heartbeatInterval*2 < leaseDuration).
func WithHeartbeatInterval(d time.Duration) Option {
	return func(e *Executor) {
		e.heartbeatInterval = d
	}
}

// WithLeaseDuration is a direct-assign option. Category: measure-then-validate.
// Sets the lease duration passed to each Heartbeat call. Zero or negative
// values are stored and rejected by NewExecutor's post-option validation
// (leaseDuration must be > 0 and heartbeatInterval*2 < leaseDuration).
func WithLeaseDuration(d time.Duration) Option {
	return func(e *Executor) {
		e.leaseDuration = d
	}
}

// withJitterSource is an internal test-only injection seam. Category: builder-noop.
// Injects a custom jitter source (e.g. a deterministic seeded source in tests).
// A nil source is silently ignored; the constructor's default random source is kept.
// Not exported because jitterSource is an unexported type; callers outside
// this package cannot construct a valid argument.
func withJitterSource(j jitterSource) Option {
	return func(e *Executor) {
		if j != nil {
			e.jitter = j
		}
	}
}
