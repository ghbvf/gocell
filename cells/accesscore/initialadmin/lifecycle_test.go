//go:build unix || windows

package initialadmin

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeLifecycleDeps constructs a BootstrapDeps suitable for lifecycle tests.
// Uses capturingHandlerCross (defined in test_helpers_crossplatform_test.go)
// so this helper compiles on both Unix and Windows.
func makeLifecycleDeps(t *testing.T) BootstrapDeps {
	t.Helper()
	logger := slog.New(&capturingHandlerCross{})
	return BootstrapDeps{
		UserRepo: mem.NewUserRepository(),
		RoleRepo: mem.NewRoleRepository(),
		Logger:   logger,
	}
}

// makeLifecycleCfgOpts returns LifecycleOptions that point at a temp dir and
// use a fast hasher so tests don't pay the bcrypt cost=12 penalty.
// Uses cross-platform scheduler and password source helpers.
func makeLifecycleCfgOpts(t *testing.T) []LifecycleOption {
	t.Helper()
	credPath := filepath.Join(t.TempDir(), "initial_admin_password")
	return []LifecycleOption{
		WithCredentialPath(credPath),
		WithTTL(time.Hour),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
		WithScheduler(newFakeSchedulerCross()),
		withPasswordSource(newFixedPasswordSourceCross()),
	}
}

// ---------------------------------------------------------------------------
// 1. NewLifecycle + Options
// ---------------------------------------------------------------------------

func TestNewLifecycle_Options(t *testing.T) {
	sched := newFakeSchedulerCross()
	clk := &fakeClock{t: time.Now()}
	h := BcryptHasher{Cost: 4}

	l := NewLifecycle(
		WithUsername("superadmin"),
		WithCredentialPath("/tmp/test_cred"),
		WithTTL(48*time.Hour),
		WithPasswordHasher(h),
		WithScheduler(sched),
		WithClock(clk),
	)

	assert.Equal(t, "superadmin", l.cfg.Username)
	assert.Equal(t, "/tmp/test_cred", l.cfg.CredentialPath)
	assert.Equal(t, 48*time.Hour, l.cfg.TTL)
	assert.Equal(t, h, l.cfg.Hasher)
	assert.Equal(t, sched, l.cfg.Scheduler)
	assert.Equal(t, clk, l.cfg.Clock)
}

func TestNewLifecycle_Defaults(t *testing.T) {
	l := NewLifecycle()

	// All config fields are zero — defaults applied later in start().
	assert.Empty(t, l.cfg.Username)
	assert.Empty(t, l.cfg.CredentialPath)
	assert.Zero(t, l.cfg.TTL)
	assert.Nil(t, l.cfg.Hasher)
	assert.Nil(t, l.cfg.Scheduler)
	assert.Nil(t, l.cfg.Clock)
	assert.False(t, l.bound)
}

// ---------------------------------------------------------------------------
// 2. Hook() returns valid hook
// ---------------------------------------------------------------------------

func TestLifecycle_Hook_Fields(t *testing.T) {
	l := NewLifecycle()
	hook := l.Hook()

	assert.Equal(t, hookName, hook.Name)
	assert.NotNil(t, hook.OnStart)
	assert.NotNil(t, hook.OnStop)
}

// ---------------------------------------------------------------------------
// 3. start without Bind fails with ErrCellInvalidConfig
// ---------------------------------------------------------------------------

func TestLifecycle_StartWithoutBind_ReturnsInvalidConfigError(t *testing.T) {
	l := NewLifecycle()
	hook := l.Hook()

	err := hook.OnStart(context.Background())
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
}

// ---------------------------------------------------------------------------
// 4. start with Bind (first run) — admin created, cleaner assigned
// ---------------------------------------------------------------------------

func TestLifecycle_StartWithBind_FirstRun_CreatesAdmin(t *testing.T) {
	opts := makeLifecycleCfgOpts(t)
	l := NewLifecycle(opts...)

	deps := makeLifecycleDeps(t)
	l.Bind(deps, deps.Logger)

	// Use a cancellable context so the cleaner's Start() (which blocks on
	// ctx.Done) returns promptly without needing the real timer to fire.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hook := l.Hook()

	// start() blocks in cleaner.Start(ctx); run in goroutine and cancel after
	// we've verified the cleaner was set.
	startDone := make(chan error, 1)
	go func() { startDone <- hook.OnStart(ctx) }()

	// Give start time to reach cleaner.Start and get assigned.
	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.cleaner != nil
	}, 2*time.Second, 10*time.Millisecond, "cleaner should be assigned after start")

	// Cancel context to unblock cleaner.Start().
	cancel()
	require.NoError(t, <-startDone)

	// Verify admin user exists in repo.
	userRepo := deps.UserRepo.(*mem.UserRepository)
	user, err := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.NotEmpty(t, user.ID)
}

// ---------------------------------------------------------------------------
// 5. start with Bind (repeat run) — admin already exists, no cleaner
// ---------------------------------------------------------------------------

func TestLifecycle_StartWithBind_RepeatRun_AdminExists_NoCleaner(t *testing.T) {
	opts := makeLifecycleCfgOpts(t)
	l := NewLifecycle(opts...)

	deps := makeLifecycleDeps(t)
	logger := deps.Logger

	// Pre-populate admin by running a bootstrap first.
	bs, err := newBootstrapper(BootstrapDeps{
		UserRepo: deps.UserRepo,
		RoleRepo: deps.RoleRepo,
		Logger:   logger,
		Clock:    l.cfg.Clock,
	}, bootstrapConfig{
		CredentialPath: l.cfg.CredentialPath,
		TTL:            l.cfg.TTL,
		PasswordSource: newFixedPasswordSourceCross(),
		Scheduler:      l.cfg.Scheduler,
		Hasher:         l.cfg.Hasher,
	})
	require.NoError(t, err)
	firstResult, err := bs.ensureAdmin(context.Background())
	require.NoError(t, err)
	firstWorker := firstResult.Cleaner
	// Stop the first cleaner so its credential file TTL doesn't interfere.
	if firstWorker != nil {
		require.NoError(t, firstWorker.Stop(context.Background()))
	}

	// Now start Lifecycle — admin exists, sweep may find a fresh cred file from
	// the first run. We remove it to ensure a pure "no orphan" scenario.
	if l.cfg.CredentialPath != "" {
		_ = removeCredentialFile(l.cfg.CredentialPath)
	}

	l.Bind(deps, logger)
	hook := l.Hook()
	err = hook.OnStart(context.Background())
	require.NoError(t, err)

	// When admin exists and no orphan file exists, no cleaner is assigned.
	l.mu.Lock()
	hasCleaner := l.cleaner != nil
	l.mu.Unlock()
	assert.False(t, hasCleaner, "no cleaner expected when admin already exists and no orphan file")
}

// ---------------------------------------------------------------------------
// 6. stop idempotent
// ---------------------------------------------------------------------------

func TestLifecycle_Stop_Idempotent(t *testing.T) {
	// Case A: stop before any start — must return nil.
	l := NewLifecycle()
	hook := l.Hook()
	assert.NoError(t, hook.OnStop(context.Background()))
	assert.NoError(t, hook.OnStop(context.Background()))
}

func TestLifecycle_Stop_AfterStart(t *testing.T) {
	opts := makeLifecycleCfgOpts(t)
	l := NewLifecycle(opts...)

	deps := makeLifecycleDeps(t)
	l.Bind(deps, deps.Logger)

	ctx, cancel := context.WithCancel(context.Background())
	hook := l.Hook()

	startDone := make(chan error, 1)
	go func() { startDone <- hook.OnStart(ctx) }()

	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.cleaner != nil
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-startDone)

	// First stop.
	assert.NoError(t, hook.OnStop(context.Background()))
	// Second stop (idempotent).
	assert.NoError(t, hook.OnStop(context.Background()))
}

// ---------------------------------------------------------------------------
// 7. stop after start-fail — cleaner must remain nil
// ---------------------------------------------------------------------------

func TestLifecycle_StartFail_CleanerRemainsNil(t *testing.T) {
	// Use an invalid TTL that will cause newBootstrapper to fail.
	l := NewLifecycle(
		WithCredentialPath(filepath.Join(t.TempDir(), "cred")),
		WithTTL(-1*time.Second), // invalid TTL
		WithPasswordHasher(BcryptHasher{Cost: 4}),
		withPasswordSource(newFixedPasswordSourceCross()),
	)

	deps := makeLifecycleDeps(t)
	l.Bind(deps, deps.Logger)

	hook := l.Hook()
	err := hook.OnStart(context.Background())
	require.Error(t, err)

	l.mu.Lock()
	hasCleaner := l.cleaner != nil
	l.mu.Unlock()
	assert.False(t, hasCleaner, "cleaner must remain nil after start failure")

	// stop must be safe even when start failed.
	assert.NoError(t, hook.OnStop(context.Background()))
}

// ---------------------------------------------------------------------------
// 8. stop-before-start race — stop() winning the lock before start() spawns
//     the cleaner goroutine must still produce a clean, nil-error state.
// ---------------------------------------------------------------------------

func TestLifecycle_StopBeforeStart_AbortsCleanlyNoGoroutineLeak(t *testing.T) {
	opts := makeLifecycleCfgOpts(t)
	l := NewLifecycle(opts...)

	deps := makeLifecycleDeps(t)
	l.Bind(deps, deps.Logger)
	hook := l.Hook()

	// stop() first — marks stopped=true.
	require.NoError(t, hook.OnStop(context.Background()))

	// start() after stop must not spawn a goroutine; it should call result.Stop
	// for the cleaner-that-would-have-run and return nil.
	require.NoError(t, hook.OnStart(context.Background()))

	l.mu.Lock()
	cleaner := l.cleaner
	done := l.done
	l.mu.Unlock()
	assert.Nil(t, cleaner, "cleaner field must stay nil after stopped start")
	assert.Nil(t, done, "done channel must stay nil — no goroutine spawned")
}

// ---------------------------------------------------------------------------
// fakeClock — used in option tests
// ---------------------------------------------------------------------------

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time { return c.t }
