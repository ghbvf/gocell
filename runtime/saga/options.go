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

// WithTracer sets the Tracer used for the per-instance `saga.coordinator.driveOne`
// span. A nil tracer is silently ignored; the Coordinator falls back to
// wrapper.NoopTracer{} (set in NewCoordinator). Must be called before Start.
//
// Category: builder-noop (cf. runtime-api.md) — tracing is an optional
// adapter wiring. The per-step `saga.executor.step.run` /
// `saga.executor.step.compensate` spans are owned by the Executor; see
// executor.WithTracer.
func WithTracer(t wrapper.Tracer) Option {
	return func(c *Coordinator) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithExecutor injects the per-step Executor. Required dependency — every
// Coordinator MUST be constructed with a non-nil Executor; the wiring layer
// owns Executor's heartbeat / retry / observer configuration. Both bare-nil
// and typed-nil are rejected at NewCoordinator with KindInvalid /
// ErrValidationFailed: "runtime/saga: executor required; pass a non-nil
// *executor.Executor via WithExecutor".
//
// Category: strong-dependency wiring option (cf. runtime-api.md). The Executor
// is not optional — it owns the per-step execution loop the Coordinator
// delegates to in driveOne.
func WithExecutor(exec *executor.Executor) Option {
	return func(c *Coordinator) {
		if exec == nil {
			c.executorNil = true
			return
		}
		c.executor = exec
	}
}
