//go:build unix

package initialadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----------------------------------------------------------------

// fixedReader is an io.Reader that always returns the same fixed content.
type fixedReader struct{ data []byte }

func (r *fixedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.data[i%len(r.data)]
	}
	return len(p), nil
}

// newFixedPasswordSource returns an io.Reader that produces a deterministic
// 32-byte sequence, yielding a known password when passed to GeneratePassword.
func newFixedPasswordSource() *fixedReader {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte('A' + (i % 26))
	}
	return &fixedReader{data: b}
}

// newBootstrapCapturingLogger returns a *slog.Logger backed by the existing
// capturingHandler type (defined in cleaner_test.go, same package).
func newBootstrapCapturingLogger() (*slog.Logger, *capturingHandler) {
	h := &capturingHandler{}
	return slog.New(h), h
}

// makeDeps returns a BootstrapDeps with mem repos and a capturing logger.
func makeDeps(t *testing.T) (BootstrapDeps, *capturingHandler) {
	t.Helper()
	logger, handler := newBootstrapCapturingLogger()
	return BootstrapDeps{
		UserRepo: mem.NewUserRepository(),
		RoleRepo: mem.NewRoleRepository(),
		Logger:   logger,
	}, handler
}

// makeCfg returns a BootstrapConfig pointing to a temp dir credential file.
func makeCfg(t *testing.T) BootstrapConfig {
	t.Helper()
	return BootstrapConfig{
		CredentialPath: filepath.Join(t.TempDir(), "initial_admin_password"),
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}
}

// knownPassword returns the password that newFixedPasswordSource() will produce.
func knownPassword(t *testing.T) string {
	t.Helper()
	pw, err := GeneratePassword(newFixedPasswordSource())
	require.NoError(t, err)
	return pw
}

// ---- fake repos for specific scenarios --------------------------------------

// duplicateUserRepo always returns ErrAuthUserDuplicate from Create.
type duplicateUserRepo struct {
	inner *mem.UserRepository
}

func (r *duplicateUserRepo) Create(_ context.Context, _ *domain.User) error {
	return errcode.New(errcode.ErrAuthUserDuplicate, "username already exists")
}
func (r *duplicateUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *duplicateUserRepo) GetByUsername(ctx context.Context, u string) (*domain.User, error) {
	return r.inner.GetByUsername(ctx, u)
}
func (r *duplicateUserRepo) Update(ctx context.Context, user *domain.User) error {
	return r.inner.Update(ctx, user)
}
func (r *duplicateUserRepo) Delete(ctx context.Context, id string) error {
	return r.inner.Delete(ctx, id)
}

var _ ports.UserRepository = (*duplicateUserRepo)(nil)

// countingRoleRepo wraps mem.RoleRepository and returns the configured count
// on the second CountByRole call (simulates another pod writing admin).
type countingRoleRepo struct {
	inner        *mem.RoleRepository
	callCount    int
	secondResult int
}

func (r *countingRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	r.callCount++
	if r.callCount >= 2 {
		return r.secondResult, nil
	}
	return r.inner.CountByRole(ctx, roleID)
}
func (r *countingRoleRepo) Create(ctx context.Context, role *domain.Role) error {
	return r.inner.Create(ctx, role)
}
func (r *countingRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	return r.inner.AssignToUser(ctx, userID, roleID)
}
func (r *countingRoleRepo) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *countingRoleRepo) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	return r.inner.GetByUserID(ctx, userID)
}
func (r *countingRoleRepo) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	return r.inner.RemoveFromUser(ctx, userID, roleID)
}
func (r *countingRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	return r.inner.RemoveFromUserIfNotLast(ctx, userID, roleID)
}

var _ ports.RoleRepository = (*countingRoleRepo)(nil)

// ---- tests ------------------------------------------------------------------

func TestBootstrap_FirstRun_CreatesUserAndWritesFile(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := makeCfg(t)
	userRepo := deps.UserRepo.(*mem.UserRepository)
	roleRepo := deps.RoleRepo.(*mem.RoleRepository)

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	cleaner, err := bs.Run(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, cleaner, "Run must return a non-nil cleaner on first bootstrap")

	// Credential file must exist.
	_, statErr := os.Stat(cfg.CredentialPath)
	assert.NoError(t, statErr, "credential file must be created")

	// File must contain username and password fields.
	contents, readErr := os.ReadFile(cfg.CredentialPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(contents), "username=admin")
	assert.Contains(t, string(contents), "password=")
	assert.Contains(t, string(contents), "expires_at=")

	// User must exist in repo.
	user, getErr := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, getErr)
	assert.True(t, strings.HasPrefix(user.ID, "usr-bootstrap-"), "user ID must have bootstrap prefix")

	// Role must be assigned.
	roles, rolesErr := roleRepo.GetByUserID(context.Background(), user.ID)
	require.NoError(t, rolesErr)
	require.Len(t, roles, 1)
	assert.Equal(t, domain.RoleAdmin, roles[0].ID)
}

func TestBootstrap_FirstRun_UserHasPasswordResetRequired(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := makeCfg(t)
	userRepo := deps.UserRepo.(*mem.UserRepository)

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, err = bs.Run(context.Background())
	require.NoError(t, err)

	user, getErr := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, getErr)
	assert.True(t, user.PasswordResetRequired, "bootstrap user must have PasswordResetRequired=true")
}

func TestBootstrap_SkipsWhenAdminExists(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := makeCfg(t)

	// Seed an admin user directly to simulate an already-bootstrapped state.
	adminUser, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$existinghash")
	require.NoError(t, err)
	adminUser.ID = "usr-existing-admin"
	require.NoError(t, deps.RoleRepo.Create(context.Background(), &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
	}))
	require.NoError(t, deps.UserRepo.Create(context.Background(), adminUser))
	_, err = deps.RoleRepo.AssignToUser(context.Background(), adminUser.ID, domain.RoleAdmin)
	require.NoError(t, err)

	// Run: should be a no-op.
	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	cleaner, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)
	assert.Nil(t, cleaner, "cleaner must be nil when admin already exists")

	// Credential file must NOT be created.
	_, statErr := os.Stat(cfg.CredentialPath)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "no credential file should be written when admin exists")
}

func TestBootstrap_PgRaceDuplicateUserSilentSkip(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	roleRepo := &countingRoleRepo{
		inner:        mem.NewRoleRepository(),
		secondResult: 1, // second CountByRole call returns 1 (another pod created admin)
	}
	deps := BootstrapDeps{
		UserRepo: &duplicateUserRepo{inner: mem.NewUserRepository()},
		RoleRepo: roleRepo,
		Logger:   logger,
	}
	cfg := BootstrapConfig{
		CredentialPath: filepath.Join(t.TempDir(), "initial_admin_password"),
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	cleaner, runErr := bs.Run(context.Background())
	require.NoError(t, runErr, "PG race duplicate should result in silent skip, not error")
	assert.Nil(t, cleaner, "cleaner must be nil on silent skip")

	// Credential file must NOT be created.
	_, statErr := os.Stat(cfg.CredentialPath)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "no credential file should be written on silent skip")
}

func TestBootstrap_FileWriteFailureFailsFast(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := BootstrapConfig{
		// Point at an impossible path (directory that cannot be created).
		CredentialPath: "/dev/null/cant-write/initial_admin_password",
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.Error(t, runErr, "file write failure must cause Run to return an error")
}

// TestBootstrap_CredFileFailureCompensatesUserAndRole verifies the F3 rollback:
// when WriteCredentialFile fails after the user + role assignment have been
// persisted, Run best-effort removes both so the next startup sees a clean
// slate (no more "user exists but nobody knows the password" dead-end).
func TestBootstrap_CredFileFailureCompensatesUserAndRole(t *testing.T) {
	deps, _ := makeDeps(t)
	userRepo := deps.UserRepo.(*mem.UserRepository)
	roleRepo := deps.RoleRepo.(*mem.RoleRepository)

	// Pass probeWriteable (dir is writable) but force WriteCredentialFile to
	// fail at os.OpenFile: pre-create a *non-empty* directory at the .tmp path.
	// credfile's "stale .tmp cleanup" os.Remove will fail on a non-empty dir,
	// and the subsequent O_EXCL|O_CREATE cannot open a directory as a file.
	dir := t.TempDir()
	credPath := filepath.Join(dir, "initial_admin_password")
	tmpDir := credPath + ".tmp"
	require.NoError(t, os.Mkdir(tmpDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "pin"), []byte("x"), 0o600))

	cfg := BootstrapConfig{
		CredentialPath: credPath,
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.Error(t, runErr, "credfile failure must surface an error")

	// Compensation: admin count back to zero so next startup re-runs bootstrap.
	count, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 0, count,
		"admin role assignment must be rolled back after credfile failure")

	// And the user row must also be gone.
	_, uerr := userRepo.GetByUsername(context.Background(), "admin")
	require.Error(t, uerr, "admin user row must be rolled back after credfile failure")
}

// TestBootstrap_OrphanUserRecoveryResumesAssign verifies the idempotent
// recovery path (Direction B, per GitLab/Keycloak/Vault patterns): if a
// previous run crashed after UserRepo.Create succeeded but before
// RoleRepo.AssignToUser committed, the next startup must:
//
//  1. detect UserRepo.Create returning ErrAuthUserDuplicate and recount==0
//  2. look up the orphan user by username
//  3. rewrite its password hash + re-mark PasswordResetRequired
//  4. resume AssignToUser on that existing ID
//  5. finish bootstrap successfully (credfile written, admin role assigned)
//
// Without this logic, the old handleUserCreateError returned an error in the
// recount==0 branch, which on PG would wedge every subsequent startup on
// "user exists but nobody knows the password". The in-memory repo masks this
// because restart wipes state, but exercising the path here keeps the
// contract honest for Phase X.
func TestBootstrap_OrphanUserRecoveryResumesAssign(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := makeCfg(t)
	userRepo := deps.UserRepo.(*mem.UserRepository)
	roleRepo := deps.RoleRepo.(*mem.RoleRepository)

	// Simulate the "previous run crashed between Create and AssignToUser"
	// state: admin username row exists, but no admin role assignment.
	orphan, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$orphanedhashFromPrevRun")
	require.NoError(t, err)
	orphan.ID = "usr-bootstrap-crashed-run"
	orphan.MarkPasswordResetRequired()
	require.NoError(t, userRepo.Create(context.Background(), orphan))
	count, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	require.Equal(t, 0, count, "precondition: no admin role assignment yet")

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	cleaner, runErr := bs.Run(context.Background())
	require.NoError(t, runErr, "bootstrap must recover from the orphan-user state, not wedge")
	assert.NotNil(t, cleaner, "cleaner must be returned after successful recovery")

	// The recovered user must keep the ORIGINAL id (we resumed the existing
	// row; we did not create a new user with a fresh UUID).
	recovered, err := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.Equal(t, "usr-bootstrap-crashed-run", recovered.ID,
		"orphan user id must be preserved — recovery resumes, does not replace")

	// Password hash must have been rewritten (new random password in credfile
	// must match the hash in the repo); the orphan hash must no longer be
	// present.
	assert.NotEqual(t, "$2a$12$orphanedhashFromPrevRun", recovered.PasswordHash,
		"orphan hash must be replaced so the credfile password matches the repo")

	// PasswordResetRequired must still be set — first login flow stays enforced.
	assert.True(t, recovered.PasswordResetRequired,
		"orphan recovery must re-assert PasswordResetRequired")

	// Admin role must now be assigned to the recovered user.
	roles, err := roleRepo.GetByUserID(context.Background(), recovered.ID)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	assert.Equal(t, domain.RoleAdmin, roles[0].ID)

	// Credential file must exist with username=admin and the fresh password.
	knownPW := knownPassword(t)
	contents, err := os.ReadFile(cfg.CredentialPath)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "username=admin")
	assert.Contains(t, string(contents), "password="+knownPW)
}

func TestBootstrap_NoPlaintextInAnyLog(t *testing.T) {
	logger, handler := newBootstrapCapturingLogger()
	deps := BootstrapDeps{
		UserRepo: mem.NewUserRepository(),
		RoleRepo: mem.NewRoleRepository(),
		Logger:   logger,
	}
	knownPW := knownPassword(t)
	cfg := BootstrapConfig{
		CredentialPath: filepath.Join(t.TempDir(), "initial_admin_password"),
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(), // same source → same password
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)

	// Walk every log record and every attribute value — none must contain the
	// plaintext password.
	handler.mu.Lock()
	defer handler.mu.Unlock()
	for _, rec := range handler.records {
		assert.NotContains(t, rec.message, knownPW,
			"log message must not contain plaintext password")
		for k, v := range rec.attrs {
			assert.NotContains(t, v, knownPW,
				"log attr %q must not contain plaintext password", k)
		}
	}
}

func TestBootstrap_RespectsCustomUsername(t *testing.T) {
	deps, _ := makeDeps(t)
	userRepo := deps.UserRepo.(*mem.UserRepository)
	cfg := BootstrapConfig{
		Username:       "root",
		CredentialPath: filepath.Join(t.TempDir(), "initial_admin_password"),
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)

	user, getErr := userRepo.GetByUsername(context.Background(), "root")
	require.NoError(t, getErr)
	assert.Equal(t, "root", user.Username)
}

func TestBootstrap_NewBootstrapperValidatesInput(t *testing.T) {
	logger, _ := newBootstrapCapturingLogger()
	validUserRepo := mem.NewUserRepository()
	validRoleRepo := mem.NewRoleRepository()

	tests := []struct {
		name    string
		deps    BootstrapDeps
		cfg     BootstrapConfig
		wantErr string
	}{
		{
			name:    "nil userRepo",
			deps:    BootstrapDeps{UserRepo: nil, RoleRepo: validRoleRepo, Logger: logger},
			cfg:     BootstrapConfig{},
			wantErr: "UserRepo",
		},
		{
			name:    "nil roleRepo",
			deps:    BootstrapDeps{UserRepo: validUserRepo, RoleRepo: nil, Logger: logger},
			cfg:     BootstrapConfig{},
			wantErr: "RoleRepo",
		},
		{
			name:    "nil logger",
			deps:    BootstrapDeps{UserRepo: validUserRepo, RoleRepo: validRoleRepo, Logger: nil},
			cfg:     BootstrapConfig{},
			wantErr: "Logger",
		},
		{
			name:    "negative TTL",
			deps:    BootstrapDeps{UserRepo: validUserRepo, RoleRepo: validRoleRepo, Logger: logger},
			cfg:     BootstrapConfig{TTL: -time.Minute},
			wantErr: "TTL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewBootstrapper(tt.deps, tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestBootstrap_FileContainsCredentialFields(t *testing.T) {
	deps, _ := makeDeps(t)
	knownPW := knownPassword(t)
	cfg := BootstrapConfig{
		Username:       "admin",
		CredentialPath: filepath.Join(t.TempDir(), "initial_admin_password"),
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)

	contents, readErr := os.ReadFile(cfg.CredentialPath)
	require.NoError(t, readErr)

	assert.Contains(t, string(contents), "username=admin")
	assert.Contains(t, string(contents), "password="+knownPW)

	// File should be mode 0600.
	info, statErr := os.Stat(cfg.CredentialPath)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestBootstrap_DefaultCredentialPathFromEnv verifies that GOCELL_STATE_DIR
// overrides the default path.
func TestBootstrap_DefaultCredentialPathFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOCELL_STATE_DIR", dir)

	deps, _ := makeDeps(t)
	cfg := BootstrapConfig{
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSource(),
		// CredentialPath intentionally left blank to exercise env-var path.
	}

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)
	assert.Equal(t, dir+"/initial_admin_password", bs.cfg.CredentialPath)

	_, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)

	_, statErr := os.Stat(filepath.Join(dir, "initial_admin_password"))
	assert.NoError(t, statErr)
}

// TestBootstrap_NoPlaintextPasswordInCfgStruct verifies that the Bootstrapper
// struct does not hold the plaintext password after Run returns.
func TestBootstrap_NoPlaintextPasswordInCfgStruct(t *testing.T) {
	deps, _ := makeDeps(t)
	cfg := makeCfg(t)
	knownPW := knownPassword(t)

	bs, err := NewBootstrapper(deps, cfg)
	require.NoError(t, err)

	_, runErr := bs.Run(context.Background())
	require.NoError(t, runErr)

	// cfg fields: Username, CredentialPath, TTL, PasswordSource, Scheduler.
	// PasswordSource is an io.Reader, not a string. Verify Username and
	// CredentialPath do not equal the password.
	assert.NotEqual(t, knownPW, bs.cfg.Username)
	assert.NotEqual(t, knownPW, bs.cfg.CredentialPath)
	assert.NotContains(t, fmt.Sprintf("%s%s", bs.cfg.Username, bs.cfg.CredentialPath), knownPW)
}
