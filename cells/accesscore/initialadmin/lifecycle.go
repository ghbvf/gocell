//go:build unix || windows

package initialadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

const hookName = "accesscore.initial-admin-bootstrap"

// errNoCleanerRequired is a sentinel returned by provisionAdmin when admin
// already exists and no orphan credential file needs cleanup, signaling the
// caller that no background cleaner worker should be launched.
var errNoCleanerRequired = errors.New("initialadmin: no cleaner required")

// config mirrors the old initialAdminConfig from cells/accesscore/cell.go:118-130.
// It is internal to the initialadmin package; exposed only through LifecycleOption.
type config struct {
	Username       string
	CredentialPath string
	TTL            time.Duration
	PasswordSource io.Reader
	Scheduler      Scheduler
	Clock          clock.Clock
	Hasher         PasswordHasher
}

// Lifecycle orchestrates first-run admin bootstrap as a single
// cell.LifecycleHook contributor. Construction is two-phase so composition
// and Cell.Init can wire orthogonal concerns:
//
//  1. NewLifecycle(opts...) — collects config via Options
//  2. Bind(deps, logger)   — injected by Cell.Init once repos are ready
//
// Hook() is safe to call before Bind; the OnStart closure reads Bind state
// at invocation time (bootstrap.Lifecycle.Start happens after Cell.Init).
//
// OnStart returns promptly after launching the cleaner in a background
// goroutine: cleaner.Start blocks on ctx.Done() waiting for TTL expiry,
// which exceeds bootstrap.Hook.StartTimeout (30s default). The goroutine
// uses an internal runCtx derived from context.WithoutCancel(ctx); OnStop
// cancels it to drain the goroutine and then calls cleaner.Stop for explicit
// timer cancellation.
type Lifecycle struct {
	cfg       config
	deps      BootstrapDeps
	logger    *slog.Logger
	cleaner   worker.Worker
	cancelRun context.CancelFunc
	done      chan struct{} // closed when cleaner goroutine exits
	mu        sync.Mutex
	bound     bool
	stopped   bool // set by stop() so a racing start() aborts cleanly
}

// LifecycleOption configures a Lifecycle instance.
type LifecycleOption func(*Lifecycle)

// WithUsername overrides the admin username (default: "admin").
func WithUsername(u string) LifecycleOption { return func(l *Lifecycle) { l.cfg.Username = u } }

// WithCredentialPath overrides the credential file path.
func WithCredentialPath(p string) LifecycleOption {
	return func(l *Lifecycle) { l.cfg.CredentialPath = p }
}

// WithTTL overrides the credential file TTL (default: 24h).
func WithTTL(d time.Duration) LifecycleOption { return func(l *Lifecycle) { l.cfg.TTL = d } }

// WithPasswordHasher overrides the bcrypt hasher. Tests inject low-cost hashers.
func WithPasswordHasher(h PasswordHasher) LifecycleOption {
	return func(l *Lifecycle) { l.cfg.Hasher = h }
}

// withPasswordSource is unexported: tests-only entropy override.
func withPasswordSource(r io.Reader) LifecycleOption {
	return func(l *Lifecycle) { l.cfg.PasswordSource = r }
}

// WithPasswordSourceForTesting overrides the entropy source used to generate
// the initial-admin password. For testing only; production uses crypto/rand.Reader.
//
// Exposed (rather than unexported withPasswordSource) so that cell-level tests
// in package accesscore can inject a deterministic source without crossing the
// internal/ boundary via an unexported symbol.
func WithPasswordSourceForTesting(r io.Reader) LifecycleOption {
	return func(l *Lifecycle) { l.cfg.PasswordSource = r }
}

// WithScheduler overrides the cleanup scheduler (tests use fakeScheduler).
func WithScheduler(s Scheduler) LifecycleOption { return func(l *Lifecycle) { l.cfg.Scheduler = s } }

// WithClock overrides the clock (tests use clockmock.FakeClock).
func WithClock(c clock.Clock) LifecycleOption { return func(l *Lifecycle) { l.cfg.Clock = c } }

// BootstrapCredentials holds the env-driven credentials for the initial admin lifecycle.
// Mirrors runtime/auth.BootstrapCredentials but defined here to avoid a circular import
// (runtime/auth must not depend on cells/).
// Batch 2 / Agent-D will wire the two types together at the composition root.
type BootstrapCredentials struct {
	Username []byte
	Password []byte
}

// WithBootstrapCredentials injects env-driven credentials for the initial admin.
// When set, the Lifecycle uses the provided username/password instead of generating
// a random password and writing it to a credential file (D3: credfile → env migration).
//
// Stub — not yet implemented. To be implemented in Batch 2 / Agent-D.
// Tests TestLifecycle_EnvDriven_* in envdriven_test.go are RED until implemented.
func WithBootstrapCredentials(_ BootstrapCredentials) LifecycleOption {
	return func(_ *Lifecycle) {
		panic("WithBootstrapCredentials: not implemented; see Batch 2 / Agent-D")
	}
}

// NewLifecycle constructs a Lifecycle with the given options. Defaults:
// Username="admin", CredentialPath resolved at start-time, TTL=24h,
// PasswordSource=crypto/rand.Reader, Hasher=bcrypt cost=12.
func NewLifecycle(opts ...LifecycleOption) *Lifecycle {
	l := &Lifecycle{}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Bind injects Cell-supplied dependencies. Must be called once from
// Cell.Init before bootstrap.Lifecycle.Start.
func (l *Lifecycle) Bind(deps BootstrapDeps, logger *slog.Logger) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deps = deps
	l.logger = logger
	l.bound = true
}

// Hook returns the cell.LifecycleHook that Bootstrap phase3b will register.
// OnStart reads l.deps at invocation time — safe to call before Bind.
func (l *Lifecycle) Hook() cell.LifecycleHook {
	return cell.LifecycleHook{
		Name:    hookName,
		OnStart: l.start,
		OnStop:  l.stop,
	}
}

func (l *Lifecycle) start(ctx context.Context) error {
	l.mu.Lock()
	if !l.bound {
		l.mu.Unlock()
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: Lifecycle.Bind must be called before Hook.OnStart runs")
	}
	deps := l.deps
	cfg := l.cfg
	// Propagate the injected clock from deps when no explicit WithClock option was set.
	if cfg.Clock == nil {
		cfg.Clock = deps.Clock
	}
	clock.MustHaveClock(cfg.Clock, "initialadmin.start")
	logger := l.logger
	l.mu.Unlock()

	result, err := l.provisionAdmin(ctx, deps, cfg, logger)
	if errors.Is(err, errNoCleanerRequired) {
		return nil // admin exists + no orphan → no cleanup needed
	}
	if err != nil {
		return err
	}
	return l.launchCleaner(ctx, result, logger)
}

// provisionAdmin runs the sweep + bootstrapper phase and returns the worker
// that should manage credential cleanup, or nil if no cleanup is needed.
func (l *Lifecycle) provisionAdmin(ctx context.Context, deps BootstrapDeps, cfg config, logger *slog.Logger) (worker.Worker, error) {
	// sweep expired orphan credential files before ensureAdmin attempts to write
	// a new one. This closes the P1-16 gap where adminExists==true caused
	// ensureAdmin to return early without cleaning orphan cred files, and also
	// prevents writeCredentialFile from failing with errCredFileExists when an
	// expired file already occupies the path.
	//
	// When a fresh (not-yet-expired) orphan file is found, sweep returns a
	// cleaner worker that will remove the file after its remaining TTL. This
	// closes the runtime window where a fresh orphan file would otherwise persist
	// until the next process restart (P1-16 full fix).
	sweepResult, err := sweep(ctx, sweepConfig{
		CredentialPath: cfg.CredentialPath,
		Clock:          cfg.Clock,
		Scheduler:      cfg.Scheduler,
		Logger:         logger,
	})
	if err != nil {
		return nil, fmt.Errorf("initialadmin: sweep: %w", err)
	}

	bs, err := newBootstrapper(BootstrapDeps{
		UserRepo: deps.UserRepo,
		RoleRepo: deps.RoleRepo,
		Logger:   logger,
		Clock:    cfg.Clock,
	}, bootstrapConfig{
		Username:       cfg.Username,
		CredentialPath: cfg.CredentialPath,
		TTL:            cfg.TTL,
		PasswordSource: cfg.PasswordSource,
		Scheduler:      cfg.Scheduler,
		Hasher:         cfg.Hasher,
	})
	if err != nil {
		return nil, fmt.Errorf("initialadmin: construct: %w", err)
	}
	adminResult, err := bs.ensureAdmin(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: ensure: %w", err)
	}
	if adminResult.Cleaner != nil {
		logger.InfoContext(ctx, "initialadmin: initial admin credentials written",
			slog.String("event", "initial_admin_credentials_written"),
			slog.String("cred_path", bs.cfg.CredentialPath))
	}

	// Priority identical to old behavior: adminWorker > sweepCleaner.
	switch {
	case adminResult.Cleaner != nil:
		return adminResult.Cleaner, nil
	case sweepResult.Cleaner != nil:
		return sweepResult.Cleaner, nil
	default:
		return nil, errNoCleanerRequired
	}
}

// launchCleaner registers result as the active worker and starts it in a
// background goroutine. A concurrent stop() racing with start() is handled by
// the stopped flag: if stop ran first, we cancel runCtx immediately and do not
// spawn the goroutine.
func (l *Lifecycle) launchCleaner(ctx context.Context, result worker.Worker, logger *slog.Logger) error {
	// Derive a long-lived runCtx that preserves caller values but is not killed
	// by bootstrap.Hook.StartTimeout (30s). OnStop cancels runCtx to drain the
	// goroutine.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		cancel()
		// Explicit Stop is idempotent and clears any registered timer that sweep
		// or ensureAdmin may have installed before stop() raced in.
		if err := result.Stop(ctx); err != nil {
			return fmt.Errorf("initialadmin: stop raced cleaner: %w", err)
		}
		return nil
	}
	l.cleaner = result
	l.cancelRun = cancel
	l.done = make(chan struct{})
	done := l.done
	l.mu.Unlock()

	go func() {
		defer close(done)
		if err := result.Start(runCtx); err != nil {
			logger.ErrorContext(runCtx, "initial admin cleaner exited with error",
				slog.String("event", "initial_admin_cleaner_error"),
				slog.Any("error", err))
		}
	}()
	return nil
}

func (l *Lifecycle) stop(ctx context.Context) error {
	l.mu.Lock()
	cleaner := l.cleaner
	cancel := l.cancelRun
	done := l.done
	l.cleaner = nil
	l.cancelRun = nil
	l.done = nil
	l.stopped = true
	l.mu.Unlock()
	if cleaner == nil {
		return nil
	}

	// Cancel runCtx so cleaner.Start returns; then explicit Stop for timer
	// cancellation (cleaner.Stop is idempotent).
	if cancel != nil {
		cancel()
	}
	stopErr := cleaner.Stop(ctx)

	// Wait for the cleaner goroutine to exit so OnStop returning signals a
	// fully-drained subsystem. Bounded by caller ctx.
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			// Caller deadline reached; return best-effort.
		}
	}
	return stopErr
}
