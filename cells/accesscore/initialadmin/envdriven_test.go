//go:build unix || windows

package initialadmin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelclock "github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Ensure auth.BootstrapCredentials and initialadmin.BootstrapCredentials are structurally
// equivalent. Batch 2 / Agent-D will unify these at the composition root.
var _ = auth.BootstrapCredentials{}

// --- SEC-SETUP-CLOSURE RED tests (Batch 0, tests 15-16) ---
// These tests verify env-driven admin creation using BootstrapCredentials.
// They are RED because:
//   1. WithBootstrapCredentials option does not exist yet (Batch 2 / Agent-D).
//   2. The Lifecycle does not accept external credentials (credfile path today).
//
// After Batch 2, the Lifecycle will accept WithBootstrapCredentials and use
// the injected username/password instead of generating a random password.

// TestLifecycle_EnvDriven_CreatesAdmin verifies that when GOCELL_BOOTSTRAP_ADMIN_*
// credentials are set and injected via WithBootstrapCredentials, the Lifecycle
// creates an admin user on startup using those credentials.
// RED: WithBootstrapCredentials option does not exist yet.
func TestLifecycle_EnvDriven_CreatesAdmin(t *testing.T) {
	deps := makeLifecycleDeps(t)
	deps.UserRepo = mem.NewUserRepository()
	deps.RoleRepo = mem.NewRoleRepository()

	creds := BootstrapCredentials{
		Username: []byte("adminop"),
		Password: []byte("secureOperatorPass1"),
	}

	l := NewLifecycle(
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
		WithBootstrapCredentials(creds), // RED: option panics until Batch 2
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
// RED: WithBootstrapCredentials option does not exist yet.
func TestLifecycle_EnvDriven_AlreadyProvisioned_NoOp(t *testing.T) {
	deps := makeLifecycleDeps(t)
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	deps.UserRepo = userRepo
	deps.RoleRepo = roleRepo

	// Seed an existing admin.
	seedAdminForLifecycleTest(t, deps)

	creds := BootstrapCredentials{
		Username: []byte("adminop"),
		Password: []byte("secureOperatorPass1"),
	}

	l := NewLifecycle(
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
		WithBootstrapCredentials(creds), // RED: option panics until Batch 2
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

// seedAdminForLifecycleTest seeds an admin in the lifecycle test repos.
func seedAdminForLifecycleTest(t *testing.T, deps BootstrapDeps) {
	t.Helper()
	// Use the lifecycle to create admin first time.
	l := NewLifecycle(
		WithClock(kernelclock.Real()),
		WithPasswordHasher(BcryptHasher{Cost: 4}),
		withPasswordSource(newFixedPasswordSourceCross()),
	)
	l.Bind(deps, deps.Logger)
	hook := l.Hook()
	require.NoError(t, hook.OnStart(context.Background()))
	require.NoError(t, hook.OnStop(context.Background()))
}
