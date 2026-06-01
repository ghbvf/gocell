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

// WithConfig replaces the entire Config struct (c.cfg = cfg). No partial
// merge, no per-field default substitution: NewCoordinator runs cfg.Validate()
// after the options loop and Validate rejects zero values for PollInterval /
// ClaimBatchSize / LeaseDuration / HeartbeatInterval. Callers that want
// defaults for unset fields must start from DefaultConfig():
//
//	cfg := saga.DefaultConfig()
//	cfg.PollInterval = 500 * time.Millisecond
//	saga.WithConfig(cfg)
//
// Must be called before Start.
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
// the Executor that NewCoordinator constructs internally.
//
// #1181 F8 typed-nil guard: WithObserver uses validation.IsNilInterface so a
// nil *SagaCollector is rejected and NopObserver is kept.
//
// For why the Coordinator owns Executor construction (rather than accepting an
// injected Executor), see runtime/saga/coordinator.go Coordinator struct
// comments (#1181 F5 design): the Executor must use the Coordinator's journal
// so claim and heartbeat are guaranteed same-source by construction, not by
// convention.
func WithObserver(o executor.Observer) Option {
	return func(c *Coordinator) {
		if !validation.IsNilInterface(o) {
			c.observer = o
		}
	}
}
