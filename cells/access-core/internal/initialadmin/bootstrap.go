//go:build unix

package initialadmin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

const (
	// defaultAdminUsername is the username created when no explicit username is provided.
	defaultAdminUsername = "admin"

	// defaultCredentialPath is used when neither BootstrapConfig.CredentialPath nor
	// GOCELL_STATE_DIR are set.
	defaultCredentialPath = "/run/gocell/initial_admin_password"

	// defaultTTL is the credential file lifetime before the cleanup worker removes it.
	defaultTTL = 24 * time.Hour
)

// BootstrapDeps holds the injected repository and utility dependencies.
type BootstrapDeps struct {
	UserRepo ports.UserRepository
	RoleRepo ports.RoleRepository
	Logger   *slog.Logger
	Clock    Clock
}

// BootstrapConfig controls the bootstrap behaviour.
type BootstrapConfig struct {
	// Username is the admin username to create. Defaults to "admin".
	Username string
	// CredentialPath is the absolute path for the credential file.
	// Defaults: GOCELL_STATE_DIR/initial_admin_password → /run/gocell/initial_admin_password.
	CredentialPath string
	// TTL is the credential file lifetime. Defaults to 24h.
	TTL time.Duration
	// PasswordSource is the entropy source for password generation.
	// Defaults to crypto/rand.Reader.
	PasswordSource io.Reader
	// Scheduler is used by the returned Cleaner. Defaults to RealScheduler{}.
	Scheduler Scheduler
}

// Bootstrapper orchestrates initial admin creation: CountByRole → generate
// password → bcrypt → create user (PasswordResetRequired=true) → assign admin
// role → WriteCredentialFile → return Cleaner worker.
type Bootstrapper struct {
	deps BootstrapDeps
	cfg  BootstrapConfig
}

// NewBootstrapper validates deps and cfg, applies defaults, and returns a
// ready Bootstrapper. Returns an error when required fields are missing or cfg
// values are invalid.
func NewBootstrapper(deps BootstrapDeps, cfg BootstrapConfig) (*Bootstrapper, error) {
	if deps.UserRepo == nil {
		return nil, fmt.Errorf("initialadmin: bootstrapper requires UserRepo")
	}
	if deps.RoleRepo == nil {
		return nil, fmt.Errorf("initialadmin: bootstrapper requires RoleRepo")
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("initialadmin: bootstrapper requires Logger")
	}
	if cfg.TTL < 0 {
		return nil, fmt.Errorf("initialadmin: bootstrapper TTL must be non-negative, got %s", cfg.TTL)
	}

	// Apply defaults.
	if cfg.Username == "" {
		cfg.Username = defaultAdminUsername
	}
	if cfg.CredentialPath == "" {
		resolved, resolveErr := resolveCredentialPath()
		if resolveErr != nil {
			return nil, resolveErr
		}
		cfg.CredentialPath = resolved
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if deps.Clock == nil {
		deps.Clock = RealClock{}
	}

	return &Bootstrapper{deps: deps, cfg: cfg}, nil
}

// ResolveCredentialPath returns the credential file path for the given stateDir.
// When stateDir is empty, the GOCELL_STATE_DIR environment variable is consulted;
// if that is also empty, the default path is used.
//
// stateDir (or GOCELL_STATE_DIR) must be an absolute path; a non-absolute value
// causes a fail-fast startup error (P2-1 fix: prevents path-traversal / ambiguous
// relative paths in credential file operations).
//
// The result is filepath.Clean'd to normalise redundant separators and ".." elements.
func ResolveCredentialPath(stateDir string) (string, error) {
	dir := stateDir
	if dir == "" {
		dir = os.Getenv("GOCELL_STATE_DIR")
	}
	if dir == "" {
		return defaultCredentialPath, nil
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("initialadmin: GOCELL_STATE_DIR must be an absolute path, got %q", dir)
	}
	return filepath.Clean(dir + "/initial_admin_password"), nil
}

// resolveCredentialPath is the internal convenience wrapper used by NewBootstrapper.
// It panics (via log.Fatal path) only in the programmatic sense; callers should
// use ResolveCredentialPath directly when they need to handle the error.
func resolveCredentialPath() (string, error) {
	return ResolveCredentialPath("")
}

// Run executes the bootstrap sequence. It is idempotent: if an admin user
// already exists (CountByRole > 0), it returns (nil, nil) without any side
// effects.
//
// Before creating the admin user, Run probes that the credential file directory
// is writable (probeWriteable). If the probe fails, Run aborts before creating
// any user, giving an actionable error at startup time.
//
// NOTE(known-limitation): if the disk fills up between user creation and
// WriteCredentialFile, the user will exist but the credential will be
// unavailable. Recovery: delete the user + role assignment rows in the DB
// manually, then restart the service. A transactional solution is deferred
// to a future PR (Cx3 scope).
//
// On success it returns a worker.Worker (Cleaner) that removes the credential
// file after the configured TTL. Callers must hand the cleaner to a lifecycle
// manager (e.g., bootstrap.WithWorkers).
func (b *Bootstrapper) Run(ctx context.Context) (worker.Worker, error) {
	// Check whether an admin already exists.
	exists, err := b.adminExists(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}

	// Pre-flight check: verify the credential directory is writable before
	// creating the admin user. This catches permission issues at startup time
	// rather than after user creation (P1-6 fix).
	if err := b.probeWriteable(); err != nil {
		return nil, fmt.Errorf("initial admin bootstrap: credential dir not writable: %w", err)
	}

	// Generate and hash password (plaintext discarded after this call).
	password, hash, err := b.generateAndHash()
	if err != nil {
		return nil, err
	}

	// Ensure admin role and create the bootstrap user.
	user, err := b.ensureRoleAndCreateUser(ctx, hash)
	if err != nil {
		return nil, err
	}
	if user == nil {
		// Concurrent bootstrap race: another replica already created admin.
		return nil, nil
	}

	// Write credential file and return cleaner.
	return b.writeFileAndMakeCleaner(password)
}

// probeWriteable verifies the credential file directory is writable by creating
// and immediately removing a temporary probe file. Returns an error with an
// actionable message if the directory is not writable or cannot be created.
// On macOS, if the path starts with /run/, the error message includes a hint
// to set GOCELL_STATE_DIR=$TMPDIR/gocell (P2-11).
func (b *Bootstrapper) probeWriteable() error {
	dir := filepath.Dir(b.cfg.CredentialPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return maybeMacOSHint(b.cfg.CredentialPath,
			fmt.Errorf("create credential directory %s: %w", dir, err))
	}
	f, err := os.CreateTemp(dir, "init-admin-probe-*")
	if err != nil {
		return maybeMacOSHint(b.cfg.CredentialPath,
			fmt.Errorf("write probe in %s: %w", dir, err))
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return nil
}

// maybeMacOSHint appends a developer hint to err when running on macOS and
// the credential path starts with /run/ (the systemd RuntimeDirectory default
// that does not exist on macOS). This surfaces actionable guidance in startup
// logs without polluting the general error path (P2-11).
func maybeMacOSHint(credPath string, err error) error {
	if runtime.GOOS == "darwin" && strings.HasPrefix(credPath, "/run/") {
		return fmt.Errorf("%w (hint: set GOCELL_STATE_DIR=$TMPDIR/gocell on macOS)", err)
	}
	return err
}

// adminExists checks if at least one user holds the admin role.
func (b *Bootstrapper) adminExists(ctx context.Context) (bool, error) {
	count, err := b.deps.RoleRepo.CountByRole(ctx, domain.RoleAdmin)
	if err != nil {
		return false, fmt.Errorf("initialadmin: count admin users: %w", err)
	}
	if count > 0 {
		b.deps.Logger.Debug("initial admin bootstrap skipped: admin already exists",
			slog.String("event", "initial_admin_bootstrap"),
			slog.Int("admin_count", count),
		)
		return true, nil
	}
	return false, nil
}

// generateAndHash creates a random password and returns (plaintext, bcrypt hash).
// The intermediate byte slice is zeroed after hashing.
func (b *Bootstrapper) generateAndHash() (password string, hash []byte, err error) {
	password, err = GeneratePassword(b.cfg.PasswordSource)
	if err != nil {
		return "", nil, fmt.Errorf("initialadmin: generate password: %w", err)
	}

	passwordBytes := []byte(password)
	hash, err = bcrypt.GenerateFromPassword(passwordBytes, domain.BcryptCost)
	// Overwrite the byte slice regardless of bcrypt success.
	for i := range passwordBytes {
		passwordBytes[i] = 0
	}
	if err != nil {
		return "", nil, fmt.Errorf("initialadmin: hash password: %w", err)
	}
	return password, hash, nil
}

// ensureRoleAndCreateUser idempotently creates the admin role and user.
// Returns nil user (no error) on a concurrent-bootstrap silent skip.
func (b *Bootstrapper) ensureRoleAndCreateUser(ctx context.Context, hash []byte) (*domain.User, error) {
	adminRole := &domain.Role{
		ID:   domain.RoleAdmin,
		Name: domain.RoleAdmin,
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	}
	if err := b.deps.RoleRepo.Create(ctx, adminRole); err != nil {
		var ecErr *errcode.Error
		if !errors.As(err, &ecErr) || ecErr.Code != errcode.ErrAuthRoleDuplicate {
			return nil, fmt.Errorf("initialadmin: ensure admin role: %w", err)
		}
		// Role already exists — continue.
	}

	user, err := domain.NewUser(
		b.cfg.Username,
		b.cfg.Username+"@gocell.local",
		string(hash),
	)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: construct user: %w", err)
	}
	user.ID = "usr-bootstrap-" + uuid.NewString()
	user.MarkPasswordResetRequired()

	if err := b.deps.UserRepo.Create(ctx, user); err != nil {
		return b.handleUserCreateError(ctx, err)
	}

	if _, err := b.deps.RoleRepo.AssignToUser(ctx, user.ID, domain.RoleAdmin); err != nil {
		return nil, fmt.Errorf("initialadmin: assign admin role: %w", err)
	}
	return user, nil
}

// handleUserCreateError handles a Create error on the user repository.
// Returns (nil, nil) for a PG concurrent-bootstrap race, or the original error.
func (b *Bootstrapper) handleUserCreateError(ctx context.Context, createErr error) (*domain.User, error) {
	var ecErr *errcode.Error
	if !errors.As(createErr, &ecErr) || ecErr.Code != errcode.ErrAuthUserDuplicate {
		return nil, fmt.Errorf("initialadmin: create user: %w", createErr)
	}
	// Duplicate user — confirm admin exists (race with another replica).
	recount, err := b.deps.RoleRepo.CountByRole(ctx, domain.RoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: recount after duplicate user: %w", err)
	}
	if recount > 0 {
		b.deps.Logger.Debug("initial admin bootstrap: duplicate user creation race, admin already exists",
			slog.String("event", "initial_admin_bootstrap"),
		)
		return nil, nil
	}
	return nil, fmt.Errorf("initialadmin: create user: %w", createErr)
}

// writeFileAndMakeCleaner writes the credential file and constructs the cleanup worker.
// IMPORTANT: password is only referenced here and inside CredentialPayload — it is not
// stored in any struct field and is not accessible after this function returns.
func (b *Bootstrapper) writeFileAndMakeCleaner(password string) (worker.Worker, error) {
	expiresAt := b.deps.Clock.Now().Add(b.cfg.TTL)
	payload := CredentialPayload{
		Username:  b.cfg.Username,
		Password:  password,
		ExpiresAt: expiresAt,
	}
	if err := WriteCredentialFile(b.cfg.CredentialPath, payload); err != nil {
		// IMPORTANT: do NOT include `password` in any log attribute below.
		// TODO(known-limitation): the user has already been created in the repo.
		// In the mem repo, there is no rollback. PG repo should wrap user creation
		// and WriteCredentialFile in a transaction; deferred to a future PR.
		b.deps.Logger.Error("initial admin bootstrap: credential file write failed; user was created but credential is unavailable",
			slog.String("event", "initial_admin_bootstrap"),
			slog.String("username", b.cfg.Username),
			slog.String("file_path", b.cfg.CredentialPath),
			slog.Any("error", err),
		)
		return nil, fmt.Errorf("initialadmin: write credential file: %w", err)
	}

	b.deps.Logger.Warn("initial admin created; retrieve credential from file and change password on first login",
		slog.String("event", "initial_admin_bootstrap"),
		slog.String("username", b.cfg.Username),
		slog.String("file_path", b.cfg.CredentialPath),
		slog.Time("expires_at", expiresAt),
	)

	cleaner, err := NewCleaner(CleanerConfig{
		Path:      b.cfg.CredentialPath,
		TTL:       b.cfg.TTL,
		Clock:     b.deps.Clock,
		Scheduler: b.cfg.Scheduler,
		Logger:    b.deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("initialadmin: create cleaner: %w", err)
	}
	return cleaner, nil
}
