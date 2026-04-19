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
	// Hasher produces the bcrypt-compatible hash stored in the user record.
	// Defaults to DefaultPasswordHasher() (bcrypt cost=12). Tests inject a
	// low-cost hasher (bcrypt.MinCost=4) to avoid blocking the startup
	// sequence — bcrypt cost=12 takes 5-7s on a slow CI runner and blocks
	// phase3→phase7 of bootstrap.Run, making /healthz appear unready.
	Hasher PasswordHasher
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
	if cfg.Hasher == nil {
		cfg.Hasher = DefaultPasswordHasher()
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

// EnsureAdmin executes the bootstrap sequence. It is idempotent: if an admin
// user already exists (CountByRole > 0), it returns (nil, nil) without any
// side effects.
//
// Before creating the admin user, EnsureAdmin probes that the credential file
// directory is writable (probeWriteable). If the probe fails, EnsureAdmin
// aborts before creating any user, giving an actionable error at startup time.
//
// Compensating rollback (F3): if WriteCredentialFile fails after the user and
// role assignment have been created, EnsureAdmin best-effort removes the role
// assignment and user before returning. This keeps the next startup on a clean
// slate (adminExists==false) instead of leaving the cluster stuck on
// "admin row exists but no one knows the password" — which previously required
// manual SQL to recover.
//
// On success it returns a worker.Worker (Cleaner) that removes the credential
// file after the configured TTL. Callers must hand the cleaner to a lifecycle
// manager (e.g., bootstrap.WithWorkers).
//
// Sweep (P1-16) is intentionally NOT called here — it is scheduled independently
// at the composition root via SweepHook so that orphan cred files are cleaned
// even when adminExists==true causes EnsureAdmin to return early.
func (b *Bootstrapper) EnsureAdmin(ctx context.Context) (worker.Worker, error) {
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

	// Write credential file and return cleaner. On failure, compensate the
	// user/role writes so the bootstrap can self-heal on next startup.
	cleaner, werr := b.writeFileAndMakeCleaner(password)
	if werr != nil {
		b.compensateAfterCredFileFailure(ctx, user.ID)
		return nil, werr
	}
	return cleaner, nil
}

// compensateAfterCredFileFailure best-effort removes the role assignment and
// user row after WriteCredentialFile fails. Errors are logged but not
// surfaced — the operator's immediate concern is the credfile failure, and a
// stale row at most forces a manual recount on next startup. We log rather
// than silent-drop so ops can spot leaked rows and clean them up explicitly.
func (b *Bootstrapper) compensateAfterCredFileFailure(ctx context.Context, userID string) {
	if err := b.deps.RoleRepo.RemoveFromUser(ctx, userID, domain.RoleAdmin); err != nil {
		b.deps.Logger.Error("initial admin bootstrap: compensating role unassign failed",
			slog.String("event", "initial_admin_bootstrap_compensate"),
			slog.String("user_id", userID),
			slog.Any("error", err))
	}
	if err := b.deps.UserRepo.Delete(ctx, userID); err != nil {
		b.deps.Logger.Error("initial admin bootstrap: compensating user delete failed",
			slog.String("event", "initial_admin_bootstrap_compensate"),
			slog.String("user_id", userID),
			slog.Any("error", err))
		return
	}
	b.deps.Logger.Warn("initial admin bootstrap: compensated after credfile failure; retry on next startup",
		slog.String("event", "initial_admin_bootstrap_compensate"),
		slog.String("user_id", userID))
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
	hash, err = b.cfg.Hasher.Hash(passwordBytes)
	// Overwrite the byte slice regardless of hashing success.
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
		existing, resolveErr := b.resolveDuplicateUser(ctx, err)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if existing == nil {
			// Silent skip: another replica finished the bootstrap concurrently.
			return nil, nil
		}
		// Orphan-user recovery: UserRepo.Create returned duplicate but no admin
		// role exists yet — a previous run crashed between Create and
		// AssignToUser. Rewrite the existing row's password hash with the
		// freshly generated one (the old hash is orphaned — we no longer know
		// the plaintext) and re-assert PasswordResetRequired, so the credfile
		// written below matches what's in the repo. Then fall through to
		// AssignToUser on the existing ID.
		existing.PasswordHash = string(hash)
		existing.MarkPasswordResetRequired()
		if err := b.deps.UserRepo.Update(ctx, existing); err != nil {
			return nil, fmt.Errorf("initialadmin: reset orphan user credentials: %w", err)
		}
		user = existing
	}

	if _, err := b.deps.RoleRepo.AssignToUser(ctx, user.ID, domain.RoleAdmin); err != nil {
		return nil, fmt.Errorf("initialadmin: assign admin role: %w", err)
	}
	return user, nil
}

// resolveDuplicateUser interprets a UserRepo.Create duplicate error into one of
// three outcomes:
//
//   - not a duplicate → return (nil, wrapped-error): caller surfaces the error
//   - duplicate + recount > 0 (another pod already completed bootstrap) →
//     return (nil, nil): caller silently skips
//   - duplicate + recount == 0 (orphan user from a previous crashed run) →
//     return (existingUser, nil): caller resumes with that user ID so
//     AssignToUser finishes the half-done bootstrap
//
// The orphan-recovery branch is the idempotent alternative to saga-style
// step-level compensation (which none of GitLab / Keycloak / Vault / Consul
// implement in their bootstrap flows). In the current in-memory repo the
// orphan state cannot persist across restarts, so this path only activates
// once we switch to PG (X1 PG-DOMAIN-REPO); having the logic in place now
// keeps the bootstrap contract consistent across the memory/PG transition.
//
// ref: GitLab db/fixtures/production/001_admin.rb (single-tx, no compensation),
// Keycloak ApplianceBootstrap.createMasterRealmAdminUser (catch duplicate,
// return false), Vault operator init (detect partial-init, resume).
func (b *Bootstrapper) resolveDuplicateUser(ctx context.Context, createErr error) (*domain.User, error) {
	var ecErr *errcode.Error
	if !errors.As(createErr, &ecErr) || ecErr.Code != errcode.ErrAuthUserDuplicate {
		return nil, fmt.Errorf("initialadmin: create user: %w", createErr)
	}
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
	// Orphan recovery: a prior run crashed after UserRepo.Create but before
	// AssignToUser committed. Pull the existing row and let the caller retry
	// AssignToUser on its ID.
	existing, err := b.deps.UserRepo.GetByUsername(ctx, b.cfg.Username)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: lookup orphan user for recovery: %w", err)
	}
	b.deps.Logger.Info("initial admin bootstrap: resuming orphan-user recovery",
		slog.String("event", "initial_admin_bootstrap_orphan_recover"),
		slog.String("user_id", existing.ID),
	)
	return existing, nil
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
		// Caller (Bootstrapper.EnsureAdmin) runs compensateAfterCredFileFailure so the
		// user + role assignment are rolled back; next startup starts clean.
		b.deps.Logger.Error("initial admin bootstrap: credential file write failed; compensating",
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
