//go:build unix || windows

package initialadmin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const hookName = "accesscore.initial-admin-bootstrap"

// config mirrors the old initialAdminConfig from cells/accesscore/cell.go.
// It is internal to the initialadmin package; exposed only through LifecycleOption.
type config struct {
	Username string
	Clock    clock.Clock
	Hasher   PasswordHasher
}

// Lifecycle orchestrates first-run admin bootstrap as a single
// cell.LifecycleHook contributor. Construction is two-phase so composition
// and Cell.Init can wire orthogonal concerns:
//
//  1. NewLifecycle(creds, opts...) — collects credentials + config via Options
//  2. Bind(deps, logger)           — injected by Cell.Init once repos are ready
//
// Hook() is safe to call before Bind; the OnStart closure reads Bind state
// at invocation time.
//
// OnStart creates the admin using the bootstrap credentials supplied at
// construction and returns immediately — no background cleaner goroutine is
// needed.
type Lifecycle struct {
	creds   BootstrapCredentials
	cfg     config
	deps    BootstrapDeps
	logger  *slog.Logger
	mu      sync.Mutex
	bound   bool
	stopped bool
}

// LifecycleOption configures a Lifecycle instance.
type LifecycleOption func(*Lifecycle)

// WithUsername overrides the admin username (default: derived from
// BootstrapCredentials.Username).
func WithUsername(u string) LifecycleOption { return func(l *Lifecycle) { l.cfg.Username = u } }

// WithPasswordHasher overrides the bcrypt hasher. Tests inject low-cost hashers.
func WithPasswordHasher(h PasswordHasher) LifecycleOption {
	return func(l *Lifecycle) { l.cfg.Hasher = h }
}

// WithClock overrides the clock (tests use clockmock.FakeClock).
func WithClock(c clock.Clock) LifecycleOption { return func(l *Lifecycle) { l.cfg.Clock = c } }

// BootstrapCredentials holds the env-driven credentials for the initial admin lifecycle.
// Mirrors runtime/auth.BootstrapCredentials but defined here to avoid a circular import
// (runtime/auth must not depend on cells/).
// The composition root (cmd/corebundle/access_module.go) wires the two types together.
type BootstrapCredentials struct {
	Username []byte
	Password []byte
}

// NewLifecycle constructs a Lifecycle with the supplied credentials and options.
//
// creds is REQUIRED — the persistent startup credential model (ADR §D9) makes
// bootstrap credentials mandatory for the lifetime of the deployment, so a
// Lifecycle without credentials is meaningless. Empty Username or Password
// fields cause the OnStart hook to fail fast.
//
// ref: docs/architecture/202605061600-adr-bootstrap-admin-boundary.md §D2 + §D9
func NewLifecycle(creds BootstrapCredentials, opts ...LifecycleOption) *Lifecycle {
	l := &Lifecycle{creds: creds}
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
	creds := l.creds
	// Propagate the injected clock from deps when no explicit WithClock option was set.
	if cfg.Clock == nil {
		cfg.Clock = deps.Clock
	}
	clock.MustHaveClock(cfg.Clock, "initialadmin.start")
	logger := l.logger
	l.mu.Unlock()

	if len(creds.Username) == 0 || len(creds.Password) == 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: NewLifecycle requires non-empty BootstrapCredentials "+
				"(Username and Password); see ADR §D2 + §D9")
	}

	return l.envDrivenProvision(ctx, deps, cfg, creds, logger)
}

// envDrivenProvision uses injected username/password to create the initial admin.
// No credfile write, no cleaner worker.
//
// Logging policy: only the "created" outcome emits a log line. The persistent
// startup credential model (ADR §D9) means already-exists is the lifecycle's
// steady state on every subsequent start — broadcasting it on every restart is
// noise.
func (l *Lifecycle) envDrivenProvision(
	ctx context.Context, deps BootstrapDeps, cfg config,
	creds BootstrapCredentials, logger *slog.Logger,
) error {
	bsDeps := BootstrapDeps{
		UserRepo: deps.UserRepo,
		RoleRepo: deps.RoleRepo,
		Logger:   logger,
		Clock:    cfg.Clock,
	}
	prov, err := newEnvDrivenBootstrapper(bsDeps, cfg.Hasher)
	if err != nil {
		return fmt.Errorf("initialadmin: env-driven: %w", err)
	}
	created, err := prov.ensureAdminFromCreds(ctx, creds)
	if err != nil {
		return fmt.Errorf("initialadmin: env-driven ensure: %w", err)
	}
	if created {
		logger.InfoContext(ctx, "initialadmin: initial admin created from env credentials",
			slog.String("event", "initial_admin_env_driven_created"),
			slog.String("username", string(creds.Username)))
	}
	return nil
}

func (l *Lifecycle) stop(_ context.Context) error {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	return nil
}
