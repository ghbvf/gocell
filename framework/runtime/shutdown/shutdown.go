// Package shutdown provides graceful shutdown support by listening for
// SIGINT/SIGTERM signals with a configurable timeout.
package shutdown

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"time"
)

// DefaultTimeout is the default graceful shutdown timeout.
const DefaultTimeout = 30 * time.Second

// Hook is a function that runs during graceful shutdown. It receives a context
// that will be canceled after the shutdown timeout expires.
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
// Hooks are called in LIFO order (last registered, first executed).
func (m *Manager) Register(h Hook) {
	m.hooks = append(m.hooks, h)
}

// Wait blocks until SIGINT or SIGTERM is received, then runs all hooks with a
// timeout-bounded context. Returns nil if all hooks reported nil error. Returns
// a joined error if any hook returned a non-nil error.
//
// Note: if the shutdown timeout expires mid-hook, the per-hook context is
// canceled. A hook that respects ctx.Done() and returns ctx.Err() will cause
// Wait to return that error via the hook's error. If all hooks return nil (they
// completed before the deadline), Wait returns nil — it does not return
// context.DeadlineExceeded on its own.
func (m *Manager) Wait() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, signalsToWatch()...)
	received := <-sig
	signal.Stop(sig)

	slog.Info("shutdown signal received", slog.String("signal", received.String()))

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	return m.runHooks(ctx)
}

// NotifyContext returns a context that is canceled when an OS shutdown signal
// is received (per-OS set: SIGINT+SIGTERM on Unix, os.Interrupt elsewhere).
// Callers must call the returned cancel func to release resources.
//
// This is the cross-platform equivalent of signal.NotifyContext(parent,
// syscall.SIGINT, syscall.SIGTERM) — the latter is incorrect on Windows
// because Windows cannot deliver SIGTERM, leaving the registration silently
// half-broken.
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, signalsToWatch()...)
}

// Shutdown runs all hooks immediately with a timeout-bounded context.
// Useful for programmatic shutdown (e.g., in tests).
func (m *Manager) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	return m.runHooks(ctx)
}

func (m *Manager) runHooks(ctx context.Context) error {
	var errs []error
	// Execute hooks in LIFO order: last registered, first executed.
	for i, v := range slices.Backward(m.hooks) {
		if err := v(ctx); err != nil {
			slog.Error("shutdown hook failed",
				slog.Int("hook_index", i),
				slog.Any("error", err),
			)
			errs = append(errs, err)
			// Continue executing remaining hooks even on failure.
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// All hooks returned nil. Do not propagate ctx.Err() here: if every hook
	// completed successfully before the deadline, the caller should see success.
	// A hook that wants to signal timeout should itself return ctx.Err().
	return nil
}
