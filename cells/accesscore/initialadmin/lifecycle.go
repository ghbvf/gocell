//go:build unix

package initialadmin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

const hookName = "accesscore.initial-admin-bootstrap"

// config mirrors the old initialAdminConfig from cells/accesscore/cell.go:118-130.
// It is internal to the initialadmin package; exposed only through LifecycleOption.
type config struct {
	Username       string
	CredentialPath string
	TTL            time.Duration
	PasswordSource io.Reader
	Scheduler      Scheduler
	Clock          Clock
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
type Lifecycle struct {
	cfg     config
	deps    BootstrapDeps
	logger  *slog.Logger
	cleaner worker.Worker
	mu      sync.Mutex
	bound   bool
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

// WithClock overrides the clock (tests use fakeClock).
func WithClock(c Clock) LifecycleOption { return func(l *Lifecycle) { l.cfg.Clock = c } }

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
		return errcode.New(errcode.ErrCellInvalidConfig,
			"initialadmin: Lifecycle.Bind must be called before Hook.OnStart runs")
	}
	deps := l.deps
	cfg := l.cfg
	logger := l.logger
	l.mu.Unlock()

	// Sweep expired orphan credential files before EnsureAdmin attempts to write
	// a new one. This closes the P1-16 gap where adminExists==true caused
	// EnsureAdmin to return early without cleaning orphan cred files, and also
	// prevents WriteCredentialFile from failing with ErrCredFileExists when an
	// expired file already occupies the path.
	//
	// When a fresh (not-yet-expired) orphan file is found, Sweep returns a
	// Cleaner worker that will remove the file after its remaining TTL. This
	// closes the runtime window where a fresh orphan file would otherwise persist
	// until the next process restart (P1-16 full fix).
	var sweepStateDir string
	if cfg.CredentialPath != "" {
		sweepStateDir = filepath.Dir(cfg.CredentialPath)
	}
	sweepCleaner, err := Sweep(ctx, SweepConfig{
		StateDir:  sweepStateDir,
		Clock:     cfg.Clock,
		Scheduler: cfg.Scheduler,
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("initialadmin: sweep: %w", err)
	}

	bs, err := NewBootstrapper(BootstrapDeps{
		UserRepo: deps.UserRepo,
		RoleRepo: deps.RoleRepo,
		Logger:   logger,
		Clock:    cfg.Clock,
	}, BootstrapConfig{
		Username:       cfg.Username,
		CredentialPath: cfg.CredentialPath,
		TTL:            cfg.TTL,
		PasswordSource: cfg.PasswordSource,
		Scheduler:      cfg.Scheduler,
		Hasher:         cfg.Hasher,
	})
	if err != nil {
		return fmt.Errorf("initialadmin: construct: %w", err)
	}
	adminWorker, err := bs.EnsureAdmin(ctx)
	if err != nil {
		return fmt.Errorf("initialadmin: ensure: %w", err)
	}

	// Priority identical to old behaviour: adminWorker > sweepCleaner.
	var result worker.Worker
	switch {
	case adminWorker != nil:
		result = adminWorker
	case sweepCleaner != nil:
		result = sweepCleaner
	}
	if result == nil {
		return nil // admin exists + no orphan → no cleanup needed
	}

	// Assign under lock before Start so stop() can reach the cleaner even if
	// Start blocks indefinitely waiting on ctx.Done().
	l.mu.Lock()
	l.cleaner = result
	l.mu.Unlock()

	// Start cleaner timer; blocks until ctx is cancelled or TTL fires.
	if err := result.Start(ctx); err != nil {
		return fmt.Errorf("initialadmin: start cleaner: %w", err)
	}
	return nil
}

func (l *Lifecycle) stop(ctx context.Context) error {
	l.mu.Lock()
	cleaner := l.cleaner
	l.cleaner = nil
	l.mu.Unlock()
	if cleaner == nil {
		return nil
	}
	return cleaner.Stop(ctx)
}
