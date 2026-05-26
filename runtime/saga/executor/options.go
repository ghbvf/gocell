package executor

import (
	"log/slog"
	"time"
)

// Option is a functional option for Executor construction.
type Option func(*Executor)

// WithLogger sets the structured logger. A nil logger is silently ignored;
// the constructor default (slog.Default()) is kept.
func WithLogger(l *slog.Logger) Option {
	return func(e *Executor) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithHeartbeatInterval sets the interval between lease heartbeat calls.
// The value is validated in NewExecutor after all options are applied.
func WithHeartbeatInterval(d time.Duration) Option {
	return func(e *Executor) {
		e.heartbeatInterval = d
	}
}

// WithLeaseDuration sets the lease duration passed to each Heartbeat call.
// The value is validated in NewExecutor after all options are applied.
func WithLeaseDuration(d time.Duration) Option {
	return func(e *Executor) {
		e.leaseDuration = d
	}
}

// WithJitterSource injects a custom jitter source (e.g., a deterministic
// seeded source in tests). A nil source is silently ignored; the
// constructor's default random source is kept.
func WithJitterSource(j jitterSource) Option {
	return func(e *Executor) {
		if j != nil {
			e.jitter = j
		}
	}
}
