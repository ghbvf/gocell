//go:build unix

package initialadmin

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fake repos for branch-coverage scenarios
// ---------------------------------------------------------------------------

// errRoleRepo returns an error from RemoveFromUser and configurable error from CountByRole.
type errRoleRepo struct {
	inner             *mem.RoleRepository
	removeFromUserErr error
	countByRoleErr    error
	countByRoleResult int
}

func (r *errRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	if r.countByRoleErr != nil {
		return 0, r.countByRoleErr
	}
	if r.countByRoleResult > 0 {
		return r.countByRoleResult, nil
	}
	return r.inner.CountByRole(ctx, roleID)
}
func (r *errRoleRepo) Create(ctx context.Context, role *domain.Role) error {
	return r.inner.Create(ctx, role)
}
func (r *errRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	return r.inner.AssignToUser(ctx, userID, roleID)
}
func (r *errRoleRepo) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *errRoleRepo) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	return r.inner.GetByUserID(ctx, userID)
}
func (r *errRoleRepo) RemoveFromUser(_ context.Context, _, _ string) error {
	return r.removeFromUserErr
}
func (r *errRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	return r.inner.RemoveFromUserIfNotLast(ctx, userID, roleID)
}
func (r *errRoleRepo) ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	return r.inner.GetByUserID(ctx, userID)
}

var _ ports.RoleRepository = (*errRoleRepo)(nil)

// errUserRepo returns configurable errors from Delete and GetByUsername.
type errUserRepo struct {
	inner            *mem.UserRepository
	deleteErr        error
	getByUsernameErr error
}

func (r *errUserRepo) Create(ctx context.Context, user *domain.User) error {
	return r.inner.Create(ctx, user)
}
func (r *errUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *errUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	if r.getByUsernameErr != nil {
		return nil, r.getByUsernameErr
	}
	return nil, fmt.Errorf("not configured")
}
func (r *errUserRepo) Update(ctx context.Context, user *domain.User) error {
	return r.inner.Update(ctx, user)
}
func (r *errUserRepo) Delete(_ context.Context, _ string) error {
	return r.deleteErr
}

var _ ports.UserRepository = (*errUserRepo)(nil)

// errOnUpdateUserRepo wraps mem.UserRepository but returns an error from Update.
type errOnUpdateUserRepo struct {
	inner     *mem.UserRepository
	updateErr error
}

func (r *errOnUpdateUserRepo) Create(ctx context.Context, user *domain.User) error {
	return r.inner.Create(ctx, user)
}
func (r *errOnUpdateUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *errOnUpdateUserRepo) GetByUsername(ctx context.Context, u string) (*domain.User, error) {
	return r.inner.GetByUsername(ctx, u)
}
func (r *errOnUpdateUserRepo) Update(_ context.Context, _ *domain.User) error {
	return r.updateErr
}
func (r *errOnUpdateUserRepo) Delete(ctx context.Context, id string) error {
	return r.inner.Delete(ctx, id)
}

var _ ports.UserRepository = (*errOnUpdateUserRepo)(nil)

// ---------------------------------------------------------------------------
// maybeMacOSHint branch coverage
// ---------------------------------------------------------------------------

// TestMaybeMacOSHint_NonDarwin verifies that on non-Darwin platforms, no hint
// is appended even when the path starts with /run/.
func TestMaybeMacOSHint_NonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("test only exercises non-Darwin branch")
	}
	base := errors.New("create dir failed")
	result := maybeMacOSHint("/run/gocell/initial_admin_password", base)
	assert.Equal(t, base, result, "non-Darwin: error must not be wrapped with macOS hint")
}

// TestMaybeMacOSHint_NonRunPath verifies that on any OS, a path not starting
// with /run/ does not get the macOS hint.
func TestMaybeMacOSHint_NonRunPath(t *testing.T) {
	base := errors.New("create dir failed")
	result := maybeMacOSHint("/var/gocell/initial_admin_password", base)
	// Regardless of OS, /var/... should not trigger the macOS hint.
	assert.False(t, strings.Contains(result.Error(), "GOCELL_STATE_DIR"),
		"non-/run/ path must not get macOS hint, got: %v", result)
}

// TestMaybeMacOSHint_Darwin verifies that on Darwin, a /run/ path gets the hint.
func TestMaybeMacOSHint_Darwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS hint only fires on darwin")
	}
	base := errors.New("create dir failed")
	result := maybeMacOSHint("/run/gocell/initial_admin_password", base)
	assert.Contains(t, result.Error(), "GOCELL_STATE_DIR",
		"darwin+/run/ path must include macOS hint")
	assert.ErrorIs(t, result, base, "hint must wrap the original error")
}

// ---------------------------------------------------------------------------
// sweep branch coverage
// ---------------------------------------------------------------------------

// TestSweep_NonAbsoluteCredentialPath_ReturnsError verifies that a relative
// CredentialPath is treated as a configuration error and returned immediately.
func TestSweep_NonAbsoluteCredentialPath_ReturnsError(t *testing.T) {
	_, err := sweep(context.Background(), sweepConfig{
		CredentialPath: "relative/path",
		Logger:         nil, // nil falls back to slog.Default
	})
	require.Error(t, err, "non-absolute CredentialPath must return an error")
	assert.Contains(t, err.Error(), "absolute")
}

// ---------------------------------------------------------------------------
// lifecycle.WithPasswordSourceForTesting branch coverage
// ---------------------------------------------------------------------------

// TestWithPasswordSourceForTesting_SetsSource verifies that the option sets the
// password source field correctly.
func TestWithPasswordSourceForTesting_SetsSource(t *testing.T) {
	src := newFixedPasswordSource()
	l := NewLifecycle(WithPasswordSourceForTesting(src))
	assert.Equal(t, src, l.cfg.PasswordSource,
		"WithPasswordSourceForTesting must set cfg.PasswordSource")
}

// ---------------------------------------------------------------------------
// generateAndHash hash error path
// ---------------------------------------------------------------------------

// errHasher always returns an error from Hash.
type errHasher struct{}

func (errHasher) Hash(_ []byte) ([]byte, error) {
	return nil, errors.New("bcrypt resource exhausted")
}

// TestGenerateAndHash_HashError exercises the error branch in generateAndHash
// when the Hasher returns an error.
func TestGenerateAndHash_HashError(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	bs := &bootstrapper{
		deps: BootstrapDeps{
			UserRepo: mem.NewUserRepository(),
			RoleRepo: mem.NewRoleRepository(),
			Logger:   logger,
		},
		cfg: bootstrapConfig{
			PasswordSource: newFixedPasswordSource(),
			Hasher:         errHasher{},
		},
	}

	_, _, err := bs.generateAndHash()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash password")
}

// ---------------------------------------------------------------------------
// newBootstrapper defaultStateDir resolution error path
// ---------------------------------------------------------------------------

// TestNewBootstrapper_ResolvesCredPathViaEnv verifies that a blank CredentialPath
// resolves from GOCELL_STATE_DIR. This exercises the defaultStateDir fallback.
func TestNewBootstrapper_ResolvesCredPathViaEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOCELL_STATE_DIR", dir)

	logger, _ := newBootstrapCapturingLogger()
	deps := BootstrapDeps{
		UserRepo: mem.NewUserRepository(),
		RoleRepo: mem.NewRoleRepository(),
		Logger:   logger,
	}
	bs, err := newBootstrapper(deps, bootstrapConfig{TTL: time.Hour})
	require.NoError(t, err)
	assert.Equal(t, dir+"/initial_admin_password", bs.cfg.CredentialPath)
}
