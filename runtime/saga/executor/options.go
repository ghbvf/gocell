package executor

import (
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/validation"
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

// WithHeartbeatInterval is a direct-assign option.
// Zero or negative values are stored and rejected by NewExecutor's post-option
// validation (direct-assign, no nil guard).
func WithHeartbeatInterval(d time.Duration) Option {
	return func(e *Executor) {
		e.heartbeatInterval = d
	}
}

// WithLeaseDuration is a direct-assign option.
// Zero or negative values are stored and rejected by NewExecutor's post-option
// validation (direct-assign, no nil guard).
func WithLeaseDuration(d time.Duration) Option {
	return func(e *Executor) {
		e.leaseDuration = d
	}
}

// WithObserverCallDeadline overrides the per-Observer-call bounded wait
// (default DefaultObserverCallDeadline = 5s). Non-positive values are silently
// ignored — the constructor default is kept. Primarily a test seam: package
// tests use a short deadline so a deliberately blocking observer surfaces the
// timeout in milliseconds rather than seconds. Production callers should rely
// on the default; the only reason to extend it is an observer with a
// known-bounded but slow remote dependency (rare). #1210 round-3 F2.
func WithObserverCallDeadline(d time.Duration) Option {
	return func(e *Executor) {
		if d > 0 {
			e.observerCallDeadline = d
		}
	}
}

// WithTracer is a cumulative builder option. Category: builder-noop.
// Sets the Tracer used for per-step Execute / Compensate spans
// (`saga.executor.step.run` / `saga.executor.step.compensate`). A nil tracer
// is silently ignored; the constructor default (wrapper.NoopTracer{}) is
// kept. Safe to call multiple times; last non-nil value wins.
//
// Builder-noop choice rationale (.claude/rules/gocell/runtime-api.md):
// Tracer is an optional adapter wiring, not a fail-fast requirement —
// NoopTracer is a correct zero-allocation default for tests and dev mode.
// Typed-nil (e.g. (*adapters/otel.Tracer)(nil)) is rejected via
// validation.IsNilInterface (#1181 F9) so a downstream tracer.Start call
// never dereferences a nil receiver.
func WithTracer(t wrapper.Tracer) Option {
	return func(e *Executor) {
		if !validation.IsNilInterface(t) {
			e.tracer = t
		}
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
// collector without forcing every test to inject one. Typed-nil (e.g. a nil
// *SagaStepCollector) is rejected via validation.IsNilInterface (#1181 F8)
// so a downstream method call never dereferences a nil receiver.
func WithObserver(o Observer) Option {
	return func(e *Executor) {
		if !validation.IsNilInterface(o) {
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
