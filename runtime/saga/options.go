package saga

import (
	"log/slog"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// Option configures a Coordinator.
type Option func(*Coordinator)

// WithDispatcher sets the post-commit kick target. Both bare-nil and typed-nil
// are silently ignored; the Coordinator falls back to NoopDispatcher (set in
// NewCoordinator). Must be called before Start.
func WithDispatcher(d Dispatcher) Option {
	return func(c *Coordinator) {
		if !validation.IsNilInterface(d) {
			c.dispatcher = d
		}
	}
}

// WithLogger replaces the structured logger. A nil l is silently ignored;
// the Coordinator falls back to slog.Default() (set in NewCoordinator). Must
// be called before Start.
func WithLogger(l *slog.Logger) Option {
	return func(c *Coordinator) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithConfig replaces the operational config. cfg is applied unconditionally;
// NewCoordinator calls cfg.Validate() after all options have run. Must be
// called before Start.
func WithConfig(cfg Config) Option {
	return func(c *Coordinator) {
		c.cfg = cfg
	}
}

// WithTracer sets the Tracer used for both the per-instance
// `saga.coordinator.driveOne` span owned by Coordinator AND the per-step
// `saga.executor.step.run` / `saga.executor.step.compensate` spans owned by
// the internally-constructed Executor. A typed-nil tracer is silently
// ignored; the Coordinator falls back to wrapper.NoopTracer{} (set in
// NewCoordinator). Must be called before Start.
//
// Category: builder-noop (cf. runtime-api.md) — tracing is an optional
// adapter wiring. Coordinator does NOT accept a separately-injected Executor;
// the same tracer reaches both layers via NewCoordinator's internal Executor
// construction, so the trace parent/child relationship is guaranteed by
// type-system construction (single source of tracing config).
func WithTracer(t wrapper.Tracer) Option {
	return func(c *Coordinator) {
		if !validation.IsNilInterface(t) {
			c.tracer = t
		}
	}
}

// WithObserver attaches an executor.Observer that receives per-step outcome /
// retry / heartbeat-failure events. A typed-nil observer is silently ignored;
// the internally-constructed Executor falls back to executor.NopObserver{}.
// Must be called before Start.
//
// Category: builder-noop (cf. runtime-api.md). The Observer is wired into
// the Executor that NewCoordinator constructs internally — there is no
// way to inject a fully-formed Executor at the Coordinator level (#1181 F5
// design decision: Executor must use the Coordinator's journal so claim and
// heartbeat are guaranteed same-source by construction, not by convention).
func WithObserver(o executor.Observer) Option {
	return func(c *Coordinator) {
		if !validation.IsNilInterface(o) {
			c.observer = o
		}
	}
}
