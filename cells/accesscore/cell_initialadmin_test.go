//go:build unix

package accesscore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// newTestReg returns a RegistryRecorder for demo mode with an empty config.
func newTestReg() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

// spinLifecycle simulates bootstrap phase3b: constructs a bootstrap.Lifecycle,
// registers the provided lifecycle hooks into it, and calls Start. The returned
// stop function triggers Stop when called (safe to defer). If Start fails,
// startErr is non-nil and stop is still safe to call.
func spinLifecycle(t *testing.T, ctx context.Context, hooks []cell.LifecycleHook) (stop func(), startErr error) {
	t.Helper()
	lc := bootstrap.NewLifecycle(bootstrap.LifecycleConfig{Clock: clock.Real()})
	for _, hook := range hooks {
		if err := lc.Append(bootstrap.Hook{
			Name:         hook.Name,
			OnStart:      hook.OnStart,
			OnStop:       hook.OnStop,
			StartTimeout: hook.StartTimeout,
			StopTimeout:  hook.StopTimeout,
		}); err != nil {
			t.Fatalf("spinLifecycle: Append failed: %v", err)
		}
	}
	startErr = lc.Start(ctx)
	stop = func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
		defer cancel()
		_ = lc.Stop(stopCtx)
	}
	return stop, startErr
}

// newTestCellWithBootstrap constructs a fully wired AccessCore using mem repos
// and the provided bootstrap credentials + lifecycle options.
//
// Pass an empty BootstrapCredentials{} to skip wiring WithInitialAdminBootstrap
// (the bootstrap-disabled scenario); WithBootstrapAuth is always wired because
// the closed contract requires it regardless of whether the lifecycle hook is
// active.
//
// Two literal NewAccessCore call sites avoid the splat path that
// CLOCK-INJECTION-TEST-CALLSITE-01 archtest cannot statically resolve.
func newTestCellWithBootstrap(
	t *testing.T,
	creds initialadmin.BootstrapCredentials,
	bootstrapOpts []initialadmin.LifecycleOption,
) *AccessCore {
	t.Helper()
	if len(creds.Username) > 0 {
		return NewAccessCore(
			WithClock(clock.Real()),
			WithUserRepository(mem.NewUserRepository()),
			WithSessionRepository(testutil.RealSessionRepo(t)),
			WithRoleRepository(mem.NewRoleRepository()),
			WithOutboxDeps(noopPublisher{}, nil),
			WithJWTIssuer(testIssuer),
			WithJWTVerifier(testVerifier),
			WithRefreshStore(newTestRefreshStore()),
			WithTxManager(durableTxRunner{}),
			WithMetricsProvider(metrics.NopProvider{}),
			WithBootstrapAuth(testPassthroughBootstrapAuth),
			WithInitialAdminBootstrap(creds, bootstrapOpts...),
		)
	}
	return NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		WithBootstrapAuth(testPassthroughBootstrapAuth),
	)
}

// defaultBootstrapCreds returns env-driven BootstrapCredentials for cell-level tests.
func defaultBootstrapCreds() initialadmin.BootstrapCredentials {
	return initialadmin.BootstrapCredentials{
		Username: []byte("admin"),
		Password: []byte("cellTestPass1"),
	}
}

// defaultBootstrapOpts returns env-driven LifecycleOptions for cell-level tests.
func defaultBootstrapOpts() []initialadmin.LifecycleOption {
	return []initialadmin.LifecycleOption{
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: 4}),
	}
}

// TestInit_WithInitialAdminBootstrap_LifecycleHookRegistered verifies that when
// no admin exists, bootstrap runs successfully via spinLifecycle and the hook
// name matches the expected constant.
func TestInit_WithInitialAdminBootstrap_LifecycleHookRegistered(t *testing.T) {
	ac := newTestCellWithBootstrap(t, defaultBootstrapCreds(), defaultBootstrapOpts())
	rec := newTestReg()
	require.NoError(t, ac.Init(context.Background(), rec))

	snap := rec.Snapshot()
	require.Len(t, snap.LifecycleHooks, 1, "WithInitialAdminBootstrap must contribute exactly one hook")
	assert.Equal(t, "accesscore.initial-admin-bootstrap", snap.LifecycleHooks[0].Name)

	stop, startErr := spinLifecycle(t, context.Background(), snap.LifecycleHooks)
	defer stop()
	require.NoError(t, startErr)
}

// TestInit_BootstrapDefaultBehaviorIsNoop verifies that when
// WithInitialAdminBootstrap is NOT set, Init succeeds without creating any
// user and LifecycleHooks returns nil.
//
// WithBootstrapAuth is still required (closed contract) and is wired with a
// passthrough middleware here.
func TestInit_BootstrapDefaultBehaviorIsNoop(t *testing.T) {
	userRepo := mem.NewUserRepository()

	ac := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		WithBootstrapAuth(testPassthroughBootstrapAuth),
	)
	rec := newTestReg()
	require.NoError(t, ac.Init(context.Background(), rec))

	snap := rec.Snapshot()
	assert.Empty(t, snap.LifecycleHooks, "LifecycleHooks must be empty when bootstrap is not configured (opt-out)")

	_, err := userRepo.GetByUsername(context.Background(), "admin")
	assert.Error(t, err, "no user should be created when bootstrap is not configured")
}

// TestInit_BootstrapAlreadyHasAdmin_NoOp verifies that when an admin already
// exists, the lifecycle hook succeeds immediately without creating a second admin.
func TestInit_BootstrapAlreadyHasAdmin_NoOp(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()

	// Pre-seed an admin.
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{
		ID: auth.RoleAdmin, Name: auth.RoleAdmin,
	}))
	adminUser, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$testhash", time.Now())
	require.NoError(t, err)
	adminUser.ID = "usr-preexisting-admin"
	require.NoError(t, userRepo.Create(context.Background(), adminUser))
	_, err = roleRepo.AssignToUser(context.Background(), adminUser.ID, auth.RoleAdmin)
	require.NoError(t, err)

	ac := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithInitialAdminBootstrap(defaultBootstrapCreds(), defaultBootstrapOpts()...),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		WithBootstrapAuth(testPassthroughBootstrapAuth),
	)
	rec := newTestReg()
	require.NoError(t, ac.Init(context.Background(), rec))

	snap := rec.Snapshot()
	stop, startErr := spinLifecycle(t, context.Background(), snap.LifecycleHooks)
	defer stop()
	require.NoError(t, startErr, "bootstrap must silently skip when admin already exists")
}

// TestInit_BootstrapUser_HasPasswordResetRequired verifies that the bootstrap
// user has PasswordResetRequired=true after the lifecycle hook runs.
func TestInit_BootstrapUser_HasPasswordResetRequired(t *testing.T) {
	userRepo := mem.NewUserRepository()

	ac := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionRepository(testutil.RealSessionRepo(t)),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithInitialAdminBootstrap(defaultBootstrapCreds(), defaultBootstrapOpts()...),
		WithTxManager(durableTxRunner{}),
		WithMetricsProvider(metrics.NopProvider{}),
		WithBootstrapAuth(testPassthroughBootstrapAuth),
	)
	rec := newTestReg()
	require.NoError(t, ac.Init(context.Background(), rec))

	snap := rec.Snapshot()
	stop, startErr := spinLifecycle(t, context.Background(), snap.LifecycleHooks)
	defer stop()
	require.NoError(t, startErr)

	user, err := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, user.PasswordResetRequired, "bootstrap user must have PasswordResetRequired=true")
}

// outbox import is already available via noopPublisher in cell_test.go (same package).
// Verify the noopPublisher is accessible.
var _ outbox.Publisher = noopPublisher{}
