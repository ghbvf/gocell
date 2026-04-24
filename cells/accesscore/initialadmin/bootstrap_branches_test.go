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
	"github.com/ghbvf/gocell/pkg/errcode"
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
// compensateAfterCredFileFailure branch coverage
// ---------------------------------------------------------------------------

// TestCompensate_BothSucceed verifies the happy-path of compensateAfterCredFileFailure:
// both RemoveFromUser and Delete succeed — only the Warn log is emitted.
func TestCompensate_BothSucceed(t *testing.T) {
	logger, handler := newBootstrapCapturingLogger()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()

	// Set up an admin user and role assignment.
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
	}))
	user, err := domain.NewUser("admin", "admin@gocell.local", "hash")
	require.NoError(t, err)
	user.ID = "usr-test-compensate"
	require.NoError(t, userRepo.Create(context.Background(), user))
	_, err = roleRepo.AssignToUser(context.Background(), user.ID, domain.RoleAdmin)
	require.NoError(t, err)

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}
	bs.compensateAfterCredFileFailure(context.Background(), user.ID)

	// Warn log must be emitted.
	rec, found := handler.findByEvent("initial_admin_bootstrap_compensate")
	assert.True(t, found, "expected compensate log record")
	assert.Equal(t, rec.attrs["user_id"], user.ID)
}

// TestCompensate_RemoveFromUserFails verifies that when RoleRepo.RemoveFromUser
// returns an error, compensateAfterCredFileFailure logs an Error and still
// attempts the user Delete.
func TestCompensate_RemoveFromUserFails(t *testing.T) {
	logger, handler := newBootstrapCapturingLogger()
	removeErr := errors.New("role repo unavailable")
	roleRepo := &errRoleRepo{inner: mem.NewRoleRepository(), removeFromUserErr: removeErr}
	userRepo := mem.NewUserRepository()

	user, err := domain.NewUser("admin", "admin@gocell.local", "hash")
	require.NoError(t, err)
	user.ID = "usr-test-remove-fail"
	require.NoError(t, userRepo.Create(context.Background(), user))

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}
	bs.compensateAfterCredFileFailure(context.Background(), user.ID)

	// Error log for RemoveFromUser failure.
	handler.mu.Lock()
	defer handler.mu.Unlock()
	hasError := false
	for _, r := range handler.records {
		if r.level.String() == "ERROR" {
			hasError = true
			break
		}
	}
	assert.True(t, hasError, "expected Error log when RemoveFromUser fails")
}

// TestCompensate_UserDeleteFails verifies that when UserRepo.Delete returns an
// error, compensateAfterCredFileFailure logs an Error (not the Warn success log).
func TestCompensate_UserDeleteFails(t *testing.T) {
	logger, handler := newBootstrapCapturingLogger()
	deleteErr := errors.New("user repo unavailable")
	roleRepo := mem.NewRoleRepository()
	userRepo := &errUserRepo{inner: mem.NewUserRepository(), deleteErr: deleteErr}

	user, err := domain.NewUser("admin", "admin@gocell.local", "hash")
	require.NoError(t, err)
	user.ID = "usr-test-delete-fail"
	require.NoError(t, userRepo.inner.Create(context.Background(), user))

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}
	bs.compensateAfterCredFileFailure(context.Background(), user.ID)

	// Error log for Delete failure.
	handler.mu.Lock()
	defer handler.mu.Unlock()
	hasError := false
	for _, r := range handler.records {
		if r.level.String() == "ERROR" && r.attrs["user_id"] == user.ID {
			hasError = true
			break
		}
	}
	assert.True(t, hasError, "expected Error log when Delete fails")
}

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
// ensureRoleAndCreateUser branch coverage
// ---------------------------------------------------------------------------

// TestEnsureRoleAndCreateUser_UpdateOrphanFails exercises the branch where
// UserRepo.Update fails during orphan-user recovery.
func TestEnsureRoleAndCreateUser_UpdateOrphanFails(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()

	innerUserRepo := mem.NewUserRepository()
	updateErr := errors.New("DB update timeout")
	userRepo := &errOnUpdateUserRepo{inner: innerUserRepo, updateErr: updateErr}

	roleRepo := mem.NewRoleRepository()

	// Pre-create an orphan user (duplicate username already in repo).
	orphan, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$orphanhash")
	require.NoError(t, err)
	orphan.ID = "usr-orphan-update-fail"
	require.NoError(t, innerUserRepo.Create(context.Background(), orphan))
	// No role assignment — orphan state.

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}

	hash := []byte("$2a$04$newhash")
	result, err := bs.ensureRoleAndCreateUser(context.Background(), hash)
	require.Error(t, err, "Update failure during orphan recovery must surface as error")
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "orphan")
}

// ---------------------------------------------------------------------------
// resolveDuplicateUser branch coverage
// ---------------------------------------------------------------------------

// TestResolveDuplicateUser_CountByRoleError exercises the branch where
// CountByRole returns an error during duplicate resolution.
func TestResolveDuplicateUser_CountByRoleError(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	countErr := errors.New("DB connection lost")
	roleRepo := &errRoleRepo{inner: mem.NewRoleRepository(), countByRoleErr: countErr}
	userRepo := mem.NewUserRepository()

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}

	dupErr := errcode.New(errcode.ErrAuthUserDuplicate, "username already exists")
	result, err := bs.resolveDuplicateUser(context.Background(), dupErr)
	require.Error(t, err, "CountByRole error must propagate")
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "recount")
}

// TestResolveDuplicateUser_GetByUsernameError exercises the branch where
// GetByUsername fails during orphan recovery (CountByRole returns 0 but lookup fails).
func TestResolveDuplicateUser_GetByUsernameError(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	getErr := errors.New("DB read error")
	userRepo := &errUserRepo{inner: mem.NewUserRepository(), getByUsernameErr: getErr}
	roleRepo := mem.NewRoleRepository()

	bs := &bootstrapper{
		deps: BootstrapDeps{UserRepo: userRepo, RoleRepo: roleRepo, Logger: logger},
		cfg:  bootstrapConfig{Username: "admin"},
	}

	dupErr := errcode.New(errcode.ErrAuthUserDuplicate, "username already exists")
	// CountByRole returns 0 (no admin yet) → triggers GetByUsername → which fails.
	result, err := bs.resolveDuplicateUser(context.Background(), dupErr)
	require.Error(t, err, "GetByUsername error during orphan recovery must surface")
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "lookup orphan")
}

// TestResolveDuplicateUser_NonDuplicateError exercises the branch where the
// create error is not ErrAuthUserDuplicate — it must be returned unwrapped.
func TestResolveDuplicateUser_NonDuplicateError(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	bs := &bootstrapper{
		deps: BootstrapDeps{
			UserRepo: mem.NewUserRepository(),
			RoleRepo: mem.NewRoleRepository(),
			Logger:   logger,
		},
		cfg: bootstrapConfig{Username: "admin"},
	}

	nonDupErr := errors.New("unexpected repo error")
	result, err := bs.resolveDuplicateUser(context.Background(), nonDupErr)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "create user")
}

// ---------------------------------------------------------------------------
// sweep branch coverage
// ---------------------------------------------------------------------------

// TestSweep_NonAbsoluteStateDir_ReturnsError verifies that a relative StateDir
// is treated as a configuration error and returned immediately.
func TestSweep_NonAbsoluteStateDir_ReturnsError(t *testing.T) {
	_, err := sweep(context.Background(), sweepConfig{
		StateDir: "relative/path",
		Logger:   nil, // nil falls back to slog.Default
	})
	require.Error(t, err, "non-absolute StateDir must return an error")
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
// adminExists error path
// ---------------------------------------------------------------------------

// TestAdminExists_CountByRoleError verifies that a CountByRole error is
// propagated through adminExists.
func TestAdminExists_CountByRoleError(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	countErr := errors.New("count error")
	roleRepo := &errRoleRepo{inner: mem.NewRoleRepository(), countByRoleErr: countErr}

	bs := &bootstrapper{
		deps: BootstrapDeps{
			UserRepo: mem.NewUserRepository(),
			RoleRepo: roleRepo,
			Logger:   logger,
		},
		cfg: bootstrapConfig{},
	}

	_, err := bs.adminExists(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count admin users")
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
