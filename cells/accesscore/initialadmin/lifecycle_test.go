//go:build unix || windows

package initialadmin

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	kernelclock "github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// makeLifecycleDeps constructs a BootstrapDeps suitable for lifecycle tests.
func makeLifecycleDeps(t *testing.T) BootstrapDeps {
	t.Helper()
	logger := slog.New(&capturingHandlerCross{})
	return BootstrapDeps{
		UserRepo: mem.NewUserRepository(),
		RoleRepo: mem.NewRoleRepository(),
		Logger:   logger,
		Clock:    kernelclock.Real(),
	}
}

// ---------------------------------------------------------------------------
// 1. NewLifecycle + Options
// ---------------------------------------------------------------------------

func TestNewLifecycle_Options(t *testing.T) {
	h := BcryptHasher{Cost: 4}
	clk := kernelclock.Real()
	creds := BootstrapCredentials{
		Username: []byte("superadmin"),
		Password: []byte("s3cretPass"),
	}

	l := NewLifecycle(
		creds,
		WithUsername("superadmin"),
		WithPasswordHasher(h),
		WithClock(clk),
	)

	assert.Equal(t, "superadmin", l.cfg.Username)
	assert.Equal(t, h, l.cfg.Hasher)
	assert.Equal(t, clk, l.cfg.Clock)
	assert.Equal(t, creds.Username, l.creds.Username)
}

func TestNewLifecycle_Defaults(t *testing.T) {
	creds := BootstrapCredentials{
		Username: []byte("admin"),
		Password: []byte("password123"),
	}
	clk := kernelclock.Real()
	l := NewLifecycle(creds, WithClock(clk))

	assert.Empty(t, l.cfg.Username)
	assert.Nil(t, l.cfg.Hasher)
	assert.Equal(t, clk, l.cfg.Clock)
	assert.Equal(t, creds.Username, l.creds.Username)
	assert.False(t, l.bound)
}

// ---------------------------------------------------------------------------
// 2. Hook() returns valid hook
// ---------------------------------------------------------------------------

func TestLifecycle_Hook_Fields(t *testing.T) {
	l := NewLifecycle(
		BootstrapCredentials{Username: []byte("a"), Password: []byte("password1")},
		WithClock(kernelclock.Real()),
	)
	hook := l.Hook()

	assert.Equal(t, hookName, hook.Name)
	assert.NotNil(t, hook.OnStart)
	assert.NotNil(t, hook.OnStop)
}

// ---------------------------------------------------------------------------
// 3. start without Bind fails with ErrCellInvalidConfig
// ---------------------------------------------------------------------------

func TestLifecycle_StartWithoutBind_ReturnsInvalidConfigError(t *testing.T) {
	l := NewLifecycle(
		BootstrapCredentials{Username: []byte("a"), Password: []byte("password1")},
		WithClock(kernelclock.Real()),
	)
	hook := l.Hook()

	err := hook.OnStart(context.Background())
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
}

// ---------------------------------------------------------------------------
// 4. start with empty BootstrapCredentials fails fast
// ---------------------------------------------------------------------------

func TestLifecycle_StartWithoutCredentials_ReturnsInvalidConfigError(t *testing.T) {
	// Empty credentials — must fail fast at OnStart.
	l := NewLifecycle(
		BootstrapCredentials{},
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	deps := makeLifecycleDeps(t)
	l.Bind(deps, deps.Logger)

	err := l.Hook().OnStart(context.Background())
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
}

// ---------------------------------------------------------------------------
// 5. env-driven: first run creates admin
// ---------------------------------------------------------------------------

func TestLifecycle_EnvDriven_FirstRun_CreatesAdmin(t *testing.T) {
	deps := makeLifecycleDeps(t)

	creds := BootstrapCredentials{
		Username: []byte("adminop"),
		Password: []byte("secureOperatorPass1"),
	}

	l := NewLifecycle(
		creds,
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	l.Bind(deps, deps.Logger)

	err := l.Hook().OnStart(context.Background())
	require.NoError(t, err, "lifecycle must start without error when credentials are valid")

	user, getUserErr := deps.UserRepo.GetByUsername(context.Background(), "adminop")
	require.NoError(t, getUserErr, "admin user must be created")
	assert.Equal(t, "adminop", user.Username)
}

// ---------------------------------------------------------------------------
// 6. env-driven: repeat run (admin already exists) is a no-op
// ---------------------------------------------------------------------------

func TestLifecycle_EnvDriven_RepeatRun_NoOp(t *testing.T) {
	deps := makeLifecycleDeps(t)

	creds := BootstrapCredentials{
		Username: []byte("adminop"),
		Password: []byte("secureOperatorPass1"),
	}

	// First run: creates admin.
	l1 := NewLifecycle(
		creds,
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	l1.Bind(deps, deps.Logger)
	require.NoError(t, l1.Hook().OnStart(context.Background()))

	// Second run: should not error.
	l2 := NewLifecycle(
		creds,
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	l2.Bind(deps, deps.Logger)
	err := l2.Hook().OnStart(context.Background())
	require.NoError(t, err, "lifecycle must not error when admin already exists")

	// Only one admin user.
	cnt, countErr := deps.RoleRepo.CountByRole(context.Background(), "admin")
	require.NoError(t, countErr)
	assert.Equal(t, 1, cnt, "admin must not be created again on second run")
}

// ---------------------------------------------------------------------------
// 7. stop is always idempotent
// ---------------------------------------------------------------------------

func TestLifecycle_Stop_Idempotent(t *testing.T) {
	l := NewLifecycle(
		BootstrapCredentials{Username: []byte("a"), Password: []byte("password1")},
		WithClock(kernelclock.Real()),
	)
	hook := l.Hook()
	assert.NoError(t, hook.OnStop(context.Background()))
	assert.NoError(t, hook.OnStop(context.Background()))
}

func TestLifecycle_Stop_AfterStart_Idempotent(t *testing.T) {
	deps := makeLifecycleDeps(t)
	creds := BootstrapCredentials{
		Username: []byte("admin"),
		Password: []byte("adminPassword1"),
	}
	l := NewLifecycle(
		creds,
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	l.Bind(deps, deps.Logger)
	hook := l.Hook()
	require.NoError(t, hook.OnStart(context.Background()))
	assert.NoError(t, hook.OnStop(context.Background()))
	assert.NoError(t, hook.OnStop(context.Background()))
}
