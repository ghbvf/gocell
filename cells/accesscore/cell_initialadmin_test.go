//go:build unix

package accesscore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFreshOrphanFile writes a minimal credential file with expires_at set to
// now+ttl in the format produced by initialadmin.formatPayload. The file is
// written with mode 0o600. Used by TestInit_BootstrapAdminExists_FreshOrphanFile.
func writeFreshOrphanFile(t *testing.T, path string, ttl time.Duration) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	expiresAt := time.Now().Add(ttl).UTC()
	content := fmt.Sprintf(
		"# GoCell initial admin credential\n"+
			"username=admin\n"+
			"password=orphan-secret\n"+
			"expires_at=%d\n",
		expiresAt.Unix(),
	)
	return os.WriteFile(path, []byte(content), 0o600)
}

// fixedReaderForCell is a simple io.Reader that returns deterministic bytes.
// Defined here because fixedReader in bootstrap_test.go lives in a different
// package (initialadmin).
type fixedReaderForCell struct{ data []byte }

func (r *fixedReaderForCell) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.data[i%len(r.data)]
	}
	return len(p), nil
}

// newCellFixedSource returns a deterministic password source for cell-level tests.
func newCellFixedSource() *fixedReaderForCell {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte('A' + (i % 26))
	}
	return &fixedReaderForCell{data: b}
}

// spinLifecycle simulates bootstrap phase3b: constructs a bootstrap.Lifecycle,
// registers AccessCore's LifecycleHooks into it, and calls Start. The returned
// stop function triggers Stop when called (safe to defer). If Start fails,
// startErr is non-nil and stop is still safe to call.
func spinLifecycle(t *testing.T, ctx context.Context, ac *AccessCore) (stop func(), startErr error) {
	t.Helper()
	lc := bootstrap.NewLifecycle(bootstrap.LifecycleConfig{})
	for _, hook := range ac.LifecycleHooks() {
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

func testDeps() cell.Dependencies {
	return cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
}

// newTestCellWithBootstrap constructs a fully wired AccessCore using mem repos
// and the provided bootstrap options.
func newTestCellWithBootstrap(
	t *testing.T,
	bootstrapOpts []initialadmin.LifecycleOption,
) *AccessCore {
	t.Helper()
	opts := []Option{
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
	}
	if len(bootstrapOpts) > 0 {
		opts = append(opts, WithInitialAdminBootstrap(bootstrapOpts...))
	}
	return NewAccessCore(opts...)
}

// TestInit_WithInitialAdminBootstrap_LifecycleHookRegistered verifies that when
// no admin exists, bootstrap runs successfully via spinLifecycle and the hook
// name matches the expected constant.
func TestInit_WithInitialAdminBootstrap_LifecycleHookRegistered(t *testing.T) {
	dir := t.TempDir()

	bootstrapOpts := []initialadmin.LifecycleOption{
		initialadmin.WithCredentialPath(filepath.Join(dir, "initial_admin_password")),
		initialadmin.WithTTL(time.Hour),
		initialadmin.WithPasswordSourceForTesting(newCellFixedSource()),
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: 4}),
	}

	ac := newTestCellWithBootstrap(t, bootstrapOpts)
	require.NoError(t, ac.Init(context.Background(), testDeps()))

	hooks := ac.LifecycleHooks()
	require.Len(t, hooks, 1, "WithInitialAdminBootstrap must contribute exactly one hook")
	assert.Equal(t, "accesscore.initial-admin-bootstrap", hooks[0].Name)

	// spinLifecycle uses a cancellable context so the cleaner's Start() returns
	// promptly once we cancel (no need to wait for real TTL).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopCh := make(chan struct{})
	var startErr error
	go func() {
		defer close(stopCh)
		var stop func()
		stop, startErr = spinLifecycle(t, ctx, ac)
		defer stop()
		// wait for test to cancel
		<-ctx.Done()
	}()

	// Give start time to reach cleaner.Start then cancel to unblock.
	time.Sleep(testtime.ShortSleep) //archtest:allow:test-sleep wait for goroutine to enter blocking cleaner.Start; no started observable
	cancel()
	<-stopCh
	require.NoError(t, startErr)
}

// TestInit_BootstrapDefaultBehaviorIsNoop verifies that when
// WithInitialAdminBootstrap is NOT set, Init succeeds without creating any
// user and LifecycleHooks returns nil.
func TestInit_BootstrapDefaultBehaviorIsNoop(t *testing.T) {
	userRepo := mem.NewUserRepository()

	ac := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	require.NoError(t, ac.Init(context.Background(), testDeps()))

	assert.Nil(t, ac.LifecycleHooks(), "LifecycleHooks must return nil when bootstrap is not configured (opt-out)")

	// No users should exist in the repo.
	_, err := userRepo.GetByUsername(context.Background(), "admin")
	assert.Error(t, err, "no user should be created when bootstrap is not configured")
}

// TestInit_BootstrapAlreadyHasAdmin_NilCleaner verifies that when an admin
// already exists and no orphan cred file is present, spinLifecycle succeeds
// without blocking (no cleaner assigned).
func TestInit_BootstrapAlreadyHasAdmin_NilCleaner(t *testing.T) {
	dir := t.TempDir()
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()

	// Pre-seed an admin.
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
	}))
	adminUser, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$testhash")
	require.NoError(t, err)
	adminUser.ID = "usr-preexisting-admin"
	require.NoError(t, userRepo.Create(context.Background(), adminUser))
	_, err = roleRepo.AssignToUser(context.Background(), adminUser.ID, domain.RoleAdmin)
	require.NoError(t, err)

	bootstrapOpts := []initialadmin.LifecycleOption{
		initialadmin.WithCredentialPath(filepath.Join(dir, "initial_admin_password")),
		initialadmin.WithTTL(time.Hour),
		initialadmin.WithPasswordSourceForTesting(newCellFixedSource()),
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: 4}),
	}

	ac := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithInitialAdminBootstrap(bootstrapOpts...),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	require.NoError(t, ac.Init(context.Background(), testDeps()))

	// Admin already exists and no orphan file → spinLifecycle returns immediately.
	stop, startErr := spinLifecycle(t, context.Background(), ac)
	defer stop()
	require.NoError(t, startErr, "bootstrap must silently skip when admin already exists")
}

// TestInit_BootstrapAdminExists_FreshOrphanFile_SweepCleanerRegistered verifies
// P1-16 full fix: when adminExists==true AND a fresh (not-yet-expired) orphan
// credential file is present, the lifecycle hook starts a sweep cleaner and the
// fresh orphan file is retained (Cleaner removes it only after its TTL fires).
//
// Execution path under test:
//  1. Sweep finds fresh orphan → returns sweepCleaner (non-nil)
//  2. EnsureAdmin sees admin already exists → returns (nil, nil)
//  3. Lifecycle.start routes to sweepCleaner → cleaner.Start(ctx)
func TestInit_BootstrapAdminExists_FreshOrphanFile_SweepCleanerRegistered(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "initial_admin_password")
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()

	// Pre-seed an admin so EnsureAdmin returns (nil, nil).
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
	}))
	adminUser, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$testhash")
	require.NoError(t, err)
	adminUser.ID = "usr-preexisting-admin"
	require.NoError(t, userRepo.Create(context.Background(), adminUser))
	_, err = roleRepo.AssignToUser(context.Background(), adminUser.ID, domain.RoleAdmin)
	require.NoError(t, err)

	// Write a fresh orphan credential file (expires_at = now + 30m).
	require.NoError(t, writeFreshOrphanFile(t, credPath, testtime.D30min))

	bootstrapOpts := []initialadmin.LifecycleOption{
		initialadmin.WithCredentialPath(credPath),
		initialadmin.WithTTL(time.Hour),
		initialadmin.WithPasswordSourceForTesting(newCellFixedSource()),
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: 4}),
	}

	ac := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithInitialAdminBootstrap(bootstrapOpts...),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	require.NoError(t, ac.Init(context.Background(), testDeps()))

	hooks := ac.LifecycleHooks()
	require.Len(t, hooks, 1)

	// Use cancellable ctx: the sweep cleaner blocks in Start until ctx.Done or TTL.
	ctx, cancel := context.WithCancel(context.Background())

	stopCh := make(chan error, 1)
	var stop func()
	go func() {
		var sErr error
		stop, sErr = spinLifecycle(t, ctx, ac)
		stopCh <- sErr
	}()

	// Wait for cleaner.Start to be reached (cleaner assigned inside start()).
	time.Sleep(testtime.ShortSleep) //archtest:allow:test-sleep wait for goroutine to enter blocking cleaner.Start; no started observable
	cancel()

	startErr := <-stopCh
	if stop != nil {
		stop()
	}
	require.NoError(t, startErr)

	// P1-16: the fresh orphan file must still be present (not yet expired).
	_, statErr := os.Stat(credPath)
	assert.NoError(t, statErr, "fresh orphan file must be retained; Cleaner removes it at runtime after TTL")
}

// TestInit_BootstrapUser_HasPasswordResetRequired verifies that the bootstrap
// user has PasswordResetRequired=true after the lifecycle hook runs.
func TestInit_BootstrapUser_HasPasswordResetRequired(t *testing.T) {
	dir := t.TempDir()
	userRepo := mem.NewUserRepository()

	bootstrapOpts := []initialadmin.LifecycleOption{
		initialadmin.WithCredentialPath(filepath.Join(dir, "initial_admin_password")),
		initialadmin.WithTTL(time.Hour),
		initialadmin.WithPasswordSourceForTesting(newCellFixedSource()),
		initialadmin.WithPasswordHasher(initialadmin.BcryptHasher{Cost: 4}),
	}

	ac := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithInitialAdminBootstrap(bootstrapOpts...),
		WithMetricsProvider(metrics.NopProvider{}),
	)
	require.NoError(t, ac.Init(context.Background(), testDeps()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopCh := make(chan struct{})
	var startErr error
	go func() {
		defer close(stopCh)
		stop, sErr := spinLifecycle(t, ctx, ac)
		startErr = sErr
		defer stop()
		<-ctx.Done()
	}()

	// Give cleaner time to start, then cancel.
	time.Sleep(testtime.ShortSleep) //archtest:allow:test-sleep wait for goroutine to enter blocking cleaner.Start; no started observable
	cancel()
	<-stopCh
	require.NoError(t, startErr)

	user, err := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, user.PasswordResetRequired, "bootstrap user must have PasswordResetRequired=true")
}

// outbox import is already available via noopPublisher in cell_test.go (same package).
// Verify the noopPublisher is accessible.
var _ outbox.Publisher = noopPublisher{}
