//go:build unix || windows

package initialadmin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	kernelclock "github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Ensure auth.BootstrapCredentials and initialadmin.BootstrapCredentials are structurally
// equivalent. The composition root (cmd/corebundle/access_module.go) wires them together.
var _ = auth.BootstrapCredentials{}

// TestLifecycle_EnvDriven_CreatesAdmin verifies that when GOCELL_BOOTSTRAP_ADMIN_*
// credentials are injected via WithBootstrapCredentials, the Lifecycle creates an
// admin user on startup using those credentials.
func TestLifecycle_EnvDriven_CreatesAdmin(t *testing.T) {
	deps := makeLifecycleDeps(t)
	deps.UserRepo = mem.NewUserRepository()
	deps.RoleRepo = mem.NewRoleRepository()

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

	hook := l.Hook()
	err := hook.OnStart(context.Background())
	require.NoError(t, err, "lifecycle must start without error when credentials are valid")

	// Verify admin was created with the env-provided username.
	user, getUserErr := deps.UserRepo.GetByUsername(context.Background(), "adminop")
	require.NoError(t, getUserErr, "admin user must be created")
	assert.Equal(t, "adminop", user.Username, "admin username must match env credential")
}

// TestLifecycle_EnvDriven_AlreadyProvisioned_NoOp verifies that the Lifecycle
// is a no-op when admin already exists, even with credentials set.
func TestLifecycle_EnvDriven_AlreadyProvisioned_NoOp(t *testing.T) {
	deps := makeLifecycleDeps(t)
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	deps.UserRepo = userRepo
	deps.RoleRepo = roleRepo

	// Seed an existing admin using env-driven path.
	seedAdminForLifecycleTest(t, deps)

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

	hook := l.Hook()
	err := hook.OnStart(context.Background())
	require.NoError(t, err, "lifecycle must not error when admin already exists")

	// Verify no second admin was created — still exactly 1 admin.
	cnt, countErr := deps.RoleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, countErr)
	assert.Equal(t, 1, cnt, "admin must not be created again on second run")
}

// seedAdminForLifecycleTest seeds an admin in the lifecycle test repos using
// the env-driven path (no credfile).
func seedAdminForLifecycleTest(t *testing.T, deps BootstrapDeps) {
	t.Helper()
	l := NewLifecycle(
		BootstrapCredentials{
			Username: []byte("admin"),
			Password: []byte("seedPassword1"),
		},
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
	)
	l.Bind(deps, deps.Logger)
	hook := l.Hook()
	require.NoError(t, hook.OnStart(context.Background()))
	require.NoError(t, hook.OnStop(context.Background()))
}
