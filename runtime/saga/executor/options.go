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

// WithObserver is a cumulative builder option. Category: builder-noop.
// Sets the Observer that receives outcome / retry / heartbeat-failure events.
// A nil observer is silently ignored; the constructor's default (NopObserver)
// is kept. Safe to call multiple times; last non-nil value wins.
//
// Builder-noop choice rationale (.claude/rules/gocell/runtime-api.md): Observer
// is an optional best-effort sink, not a wiring-required dependency. NopObserver
// is a correct zero-value default. New composition roots may attach a metrics
// collector without forcing every test to inject one.
func WithObserver(o Observer) Option {
	return func(e *Executor) {
		if o != nil {
			e.observer = o
		}
	}
}

// withJitterSource is an internal test-only injection seam. Category: builder-noop.
// Injects a custom jitter source (e.g. a fixed-seed source for exact-value
// backoff assertions in package tests). A nil source is silently ignored; the
// constructor's default random source is kept.
//
// Intentionally unexported — there is no production or external consumer.
// The default jitter is seeded from the injected clock (newDefaultJitter uses
// clk.Now()), so external callers already get a reproducible backoff sequence
// under a fixed FakeClock without injecting anything; only in-package tests that
// assert exact jitter values need this seam. Exporting it would add zero-consumer
// public API surface (and jitterSource is unexported, so the parameter type
// would be unnameable by callers anyway).
func withJitterSource(j jitterSource) Option {
	return func(e *Executor) {
		if j != nil {
			e.jitter = j
		}
	}
}
