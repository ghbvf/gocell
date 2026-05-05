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
	Username       string
	Clock          clock.Clock
	Hasher         PasswordHasher
	// bootstrapCreds holds the env-driven credentials for the initial admin.
	// Required: NewLifecycle is a no-op if nil when start() is called.
	bootstrapCreds *BootstrapCredentials
}

// Lifecycle orchestrates first-run admin bootstrap as a single
// cell.LifecycleHook contributor. Construction is two-phase so composition
// and Cell.Init can wire orthogonal concerns:
//
//  1. NewLifecycle(opts...) — collects config via Options
//  2. Bind(deps, logger)   — injected by Cell.Init once repos are ready
//
// Hook() is safe to call before Bind; the OnStart closure reads Bind state
// at invocation time.
//
// OnStart creates the admin using the injected bootstrap credentials and
// returns immediately — no background cleaner goroutine is needed.
type Lifecycle struct {
	cfg     config
	deps    BootstrapDeps
	logger  *slog.Logger
	mu      sync.Mutex
	bound   bool
	stopped bool
}

// LifecycleOption configures a Lifecycle instance.
type LifecycleOption func(*Lifecycle)

// WithUsername overrides the admin username (default: "admin").
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

// WithBootstrapCredentials injects env-driven credentials for the initial admin.
// When set, the Lifecycle uses the provided username/password directly: username
// defines the admin identity, password is hashed and stored. No credential file
// is written and no TTL cleaner is registered — the operator manages credential
// hygiene via the env vars.
//
// ref: keycloak/keycloak KC_BOOTSTRAP_ADMIN_USERNAME (one-shot env, no credfile)
func WithBootstrapCredentials(creds BootstrapCredentials) LifecycleOption {
	return func(l *Lifecycle) {
		l.cfg.bootstrapCreds = &creds
	}
}

// NewLifecycle constructs a Lifecycle with the given options.
// WithBootstrapCredentials is required; start() will fail-fast if it is absent.
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

	if cfg.bootstrapCreds == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: WithBootstrapCredentials is required; no credfile path is supported")
	}

	return l.envDrivenProvision(ctx, deps, cfg, logger)
}

// envDrivenProvision uses injected username/password to create the initial admin.
// No credfile write, no cleaner worker.
func (l *Lifecycle) envDrivenProvision(ctx context.Context, deps BootstrapDeps, cfg config, logger *slog.Logger) error {
	creds := cfg.bootstrapCreds
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
	outcome, err := prov.ensureAdminFromCreds(ctx, creds)
	if err != nil {
		return fmt.Errorf("initialadmin: env-driven ensure: %w", err)
	}
	switch outcome {
	case envDrivenOutcomeCreated:
		logger.InfoContext(ctx, "initialadmin: initial admin created from env credentials",
			slog.String("event", "initial_admin_env_driven_created"),
			slog.String("username", string(creds.Username)))
	case envDrivenOutcomeAlreadyExists:
		// Admin already exists. Warn if bootstrap creds are still set — operator
		// should remove the env vars once the admin has been provisioned.
		logger.Warn("bootstrap credentials present after admin provisioned; "+
			"consider removing GOCELL_BOOTSTRAP_ADMIN_* env for hygiene",
			slog.String("event", "bootstrap_creds_hygiene_warn"),
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
