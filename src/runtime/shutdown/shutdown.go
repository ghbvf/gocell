// Package shutdown provides graceful shutdown support by listening for
// SIGINT/SIGTERM signals with a configurable timeout.
package shutdown

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// DefaultTimeout is the default graceful shutdown timeout.
const DefaultTimeout = 30 * time.Second

// Hook is a function that runs during graceful shutdown. It receives a context
// that will be cancelled after the shutdown timeout expires.
type Hook func(ctx context.Context) error

// Manager coordinates graceful shutdown.
type Manager struct {
	timeout time.Duration
	hooks   []Hook
}

// Option configures a Manager.
type Option func(*Manager)

// WithTimeout sets the shutdown timeout.
func WithTimeout(d time.Duration) Option {
	return func(m *Manager) {
		m.timeout = d
	}
}

// New creates a Manager with the given options.
func New(opts ...Option) *Manager {
	m := &Manager{timeout: DefaultTimeout}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Register adds a hook that will be called during shutdown.
// Hooks are called in registration order.
func (m *Manager) Register(h Hook) {
	m.hooks = append(m.hooks, h)
}

// Wait blocks until SIGINT or SIGTERM is received, then runs all hooks with a
// timeout-bounded context. If all hooks complete before the timeout, it returns
// nil. If the timeout expires, it returns context.DeadlineExceeded.
func (m *Manager) Wait() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig
	signal.Stop(sig)

	slog.Info("shutdown signal received", slog.String("signal", received.String()))

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	return m.runHooks(ctx)
}

// Shutdown runs all hooks immediately with a timeout-bounded context.
// Useful for programmatic shutdown (e.g., in tests).
func (m *Manager) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	return m.runHooks(ctx)
}

func (m *Manager) runHooks(ctx context.Context) error {
	for i, h := range m.hooks {
		if err := h(ctx); err != nil {
			slog.Error("shutdown hook failed",
				slog.Int("hook_index", i),
				slog.Any("error", err),
			)
			return err
		}
	}
	return ctx.Err()
}
