package initialadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ghbvf/gocell/runtime/worker"
)

// Compile-time assertion: cleaner must implement worker.Worker.
var _ worker.Worker = (*cleaner)(nil)

// cleanerState tracks the lifecycle of a cleaner.
type cleanerState int

const (
	stateIdle    cleanerState = iota // not yet started
	stateRunning                     // Start called, timer registered
	stateStopped                     // Stop called or ctx cancelled
)

// cleanerConfig holds the configuration for creating a cleaner.
type cleanerConfig struct {
	// Path is the credential file path to remove when TTL expires. Required.
	Path string
	// TTL is the duration after Start before the credential file is removed. Required; must be > 0.
	TTL time.Duration
	// Clock is optional; defaults to realClock{}.
	Clock Clock
	// Scheduler is optional; defaults to realScheduler{}.
	Scheduler Scheduler
	// Logger is required.
	Logger *slog.Logger
}

// cleaner implements worker.Worker. It removes the initial-admin credential
// file after a configurable TTL, protecting against credential leakage in
// long-running deployments.
//
// Lifecycle:
//   - Start(ctx): registers an AfterFunc timer; returns when ctx is cancelled
//     (early stop) or immediately if already stopped.
//   - Stop(ctx): cancels the pending timer; idempotent.
//   - Start after Stop returns an error (no reuse).
type cleaner struct {
	path      string
	ttl       time.Duration
	clock     Clock
	scheduler Scheduler
	logger    *slog.Logger

	mu        sync.Mutex
	state     cleanerState
	canceller Cancellable // non-nil between Start and expiry/stop
}

// newCleaner constructs a cleaner from cfg. Returns an error if path is empty,
// TTL ≤ 0, or logger is nil.
func newCleaner(cfg cleanerConfig) (*cleaner, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("initialadmin: cleaner path must not be empty")
	}
	if cfg.TTL <= 0 {
		return nil, fmt.Errorf("initialadmin: cleaner TTL must be positive, got %s", cfg.TTL)
	}
	if cfg.Logger == nil {
		return nil, fmt.Errorf("initialadmin: cleaner logger must not be nil")
	}

	clk := cfg.Clock
	if clk == nil {
		clk = realClock{}
	}
	sched := cfg.Scheduler
	if sched == nil {
		sched = realScheduler{}
	}

	return &cleaner{
		path:      cfg.Path,
		ttl:       cfg.TTL,
		clock:     clk,
		scheduler: sched,
		logger:    cfg.Logger,
		state:     stateIdle,
	}, nil
}

// Start recovers the remaining TTL from the credential file's expires_at field
// and registers a timer for that duration, then blocks until ctx is cancelled
// or the timer fires. If the credential file is missing, Start logs at Info and
// returns immediately (operator-managed cleanup). If the TTL has already elapsed,
// expire() is called synchronously before Start blocks.
//
// If Stop was already called before Start, an error is returned immediately.
func (c *cleaner) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.state == stateStopped {
		c.mu.Unlock()
		return fmt.Errorf("initialadmin: cleaner cannot be restarted after Stop")
	}
	if c.state == stateRunning {
		c.mu.Unlock()
		return fmt.Errorf("initialadmin: cleaner is already running")
	}
	c.mu.Unlock()

	// Recover remaining TTL from the credential file. This handles process
	// restarts: the TTL is always measured from the file's expires_at timestamp,
	// not from the current process start time.
	remaining, err := c.resolveRemaining()
	if err != nil {
		// File missing — operator already removed it or it never existed.
		c.logger.Info("initial admin credential file not found; no cleanup needed",
			slog.String("event", "initial_admin_credential_expired"),
			slog.String("path", c.path),
		)
		c.mu.Lock()
		c.state = stateStopped
		c.mu.Unlock()
		return nil
	}

	c.mu.Lock()
	if remaining <= 0 {
		// Already expired — call expire synchronously then return.
		c.state = stateRunning
		c.mu.Unlock()
		c.expire()
		return nil
	}

	// Register the expiry callback with the recovered remaining duration.
	c.canceller = c.scheduler.AfterFunc(remaining, c.expire)
	c.state = stateRunning
	c.mu.Unlock()

	// Block until context is cancelled (early stop path).
	<-ctx.Done()

	// Context cancelled — cancel the timer if it hasn't fired yet.
	c.mu.Lock()
	if c.state == stateRunning && c.canceller != nil {
		c.canceller.Stop()
		c.state = stateStopped
	}
	c.mu.Unlock()

	return nil
}

// resolveRemaining reads the expires_at from the credential file and returns
// the duration until expiry (may be negative if already expired).
// Returns an error when the file does not exist or cannot be parsed.
func (c *cleaner) resolveRemaining() (time.Duration, error) {
	expiresAt, err := readCredentialExpiresAt(c.path)
	if err != nil {
		return 0, err
	}
	return expiresAt.Sub(c.clock.Now()), nil
}

// Stop cancels the pending TTL timer. It is safe to call multiple times
// (idempotent). If the timer has already fired, Stop has no effect on the
// removal that already occurred.
func (c *cleaner) Stop(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == stateRunning && c.canceller != nil {
		c.canceller.Stop()
	}
	c.state = stateStopped
	return nil
}

// expire is the internal callback invoked by the scheduler after TTL elapses.
// It removes the credential file and logs the outcome.
func (c *cleaner) expire() {
	// Check existence before removal to distinguish "removed now" from
	// "already gone by operator" — both result in nil from removeCredentialFile,
	// but we log at different levels.
	_, statErr := os.Stat(c.path)
	fileExisted := statErr == nil

	err := removeCredentialFile(c.path)

	c.mu.Lock()
	c.state = stateStopped
	c.mu.Unlock()

	switch {
	case errors.Is(err, errCredFileTampered):
		// File permissions were tampered, but removeCredentialFile still deleted
		// the file before returning (P1-1 fix). Log at Warn: credential has been
		// destroyed, but the anomaly warrants operator attention.
		c.logger.Warn("initial admin credential file had unexpected mode; deleted anyway (tamper detected)",
			slog.String("event", "initial_admin_credential_expired"),
			slog.String("path", c.path),
			slog.Any("error", err),
		)
	case err != nil:
		// Unexpected removal error.
		c.logger.Error("initial admin credential file removal failed",
			slog.String("event", "initial_admin_credential_expired"),
			slog.String("path", c.path),
			slog.Any("error", err),
		)
	case fileExisted:
		// File was present and successfully removed — expected expiry path.
		c.logger.Warn("initial admin credential file expired and was removed",
			slog.String("event", "initial_admin_credential_expired"),
			slog.String("path", c.path),
		)
	default:
		// File was already gone (operator-removed) — idempotent, log at Info.
		c.logger.Info("initial admin credential file already removed by operator",
			slog.String("event", "initial_admin_credential_expired"),
			slog.String("path", c.path),
		)
	}
}
