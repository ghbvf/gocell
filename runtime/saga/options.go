package saga

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/validation"
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
