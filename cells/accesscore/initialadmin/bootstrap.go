//go:build unix || windows

package initialadmin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

const (
	// defaultAdminUsername is the username created when no explicit username is provided.
	defaultAdminUsername = "admin"

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

// bootstrapConfig controls the bootstrap behaviour.
type bootstrapConfig struct {
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
	// Scheduler is used by the returned cleaner. Defaults to realScheduler{}.
	Scheduler Scheduler
	// Hasher produces the bcrypt-compatible hash stored in the user record.
	// Defaults to defaultPasswordHasher() (bcrypt cost=12). Tests inject a
	// low-cost hasher (bcrypt.MinCost=4) to avoid blocking the startup
	// sequence — bcrypt cost=12 takes 5-7s on a slow CI runner and blocks
	// phase3→phase7 of bootstrap.Run, making /healthz appear unready.
	Hasher PasswordHasher
}

// bootstrapper orchestrates initial admin creation: delegate race-safe user+role
// persistence to adminprovision → writeCredentialFile → return cleaner worker.
//
// Race / orphan / duplicate semantics live in adminprovision.Provisioner; this
// layer owns only the credfile write + cleanup worker + probeWriteable.
type bootstrapper struct {
	deps        BootstrapDeps
	cfg         bootstrapConfig
	provisioner *adminprovision.Provisioner
}

// newBootstrapper validates deps and cfg, applies defaults, and returns a
// ready bootstrapper. Returns an error when required fields are missing or cfg
// values are invalid.
func newBootstrapper(deps BootstrapDeps, cfg bootstrapConfig) (*bootstrapper, error) {
	if deps.UserRepo == nil {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "initialadmin: bootstrapper requires UserRepo")
	}
	if deps.RoleRepo == nil {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "initialadmin: bootstrapper requires RoleRepo")
	}
	if deps.Logger == nil {
		return nil, errcode.New(errcode.ErrCellInvalidConfig, "initialadmin: bootstrapper requires Logger")
	}
	if cfg.TTL < 0 {
		return nil, errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf("initialadmin: bootstrapper TTL must be non-negative, got %s", cfg.TTL))
	}

	// Apply defaults.
	if cfg.Username == "" {
		cfg.Username = defaultAdminUsername
	}
	if cfg.CredentialPath == "" {
		resolved, resolveErr := ResolveCredentialPath("")
		if resolveErr != nil {
			return nil, errcode.Wrap(errcode.ErrCellInvalidConfig,
				"initialadmin: resolve credential path", resolveErr)
		}
		cfg.CredentialPath = resolved
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if deps.Clock == nil {
		deps.Clock = realClock{}
	}
	if cfg.Hasher == nil {
		cfg.Hasher = defaultPasswordHasher()
	}

	prov, err := adminprovision.NewProvisioner(deps.UserRepo, deps.RoleRepo, deps.Logger, uuid.NewString)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: build provisioner: %w", err)
	}
	return &bootstrapper{deps: deps, cfg: cfg, provisioner: prov}, nil
}

// ensureAdmin executes the bootstrap sequence. It is idempotent: if an admin
// user already exists (CountByRole > 0), it returns (nil, nil) without any
// side effects.
//
// Before creating the admin user, ensureAdmin probes that the credential file
// directory is writable (probeWriteable). If the probe fails, ensureAdmin
// aborts before creating any user, giving an actionable error at startup time.
//
// Compensating rollback (F3): if writeCredentialFile fails after the user and
// role assignment have been created, ensureAdmin best-effort removes the role
// assignment and user before returning. This keeps the next startup on a clean
// slate (adminExists==false) instead of leaving the cluster stuck on
// "admin row exists but no one knows the password" — which previously required
// manual SQL to recover.
//
// On success it returns a worker.Worker (cleaner) that removes the credential
// file after the configured TTL. Callers must hand the cleaner to a lifecycle
// manager (e.g., bootstrap.WithWorkers).
//
// sweep (P1-16) is intentionally NOT called here — it is scheduled independently
// at the composition root via SweepHook so that orphan cred files are cleaned
// even when adminExists==true causes ensureAdmin to return early.
func (b *bootstrapper) ensureAdmin(ctx context.Context) (ensureAdminResult, error) {
	// Pre-flight: fail fast if admin already exists (no credfile probe needed).
	exists, err := b.provisioner.Status(ctx)
	if err != nil {
		return ensureAdminResult{}, fmt.Errorf("initialadmin: check status: %w", err)
	}
	if exists {
		return ensureAdminResult{}, nil
	}

	// Pre-flight: verify the credential directory is writable before creating
	// the admin user. This catches permission issues at startup time rather
	// than after user creation (P1-6 fix).
	if err := b.probeWriteable(); err != nil {
		return ensureAdminResult{}, fmt.Errorf("initial admin bootstrap: credential dir not writable: %w", err)
	}

	// Generate and hash password (plaintext discarded after this call).
	password, hash, err := b.generateAndHash()
	if err != nil {
		return ensureAdminResult{}, err
	}

	// Delegate race-safe persistence to adminprovision.
	result, err := b.provisioner.Ensure(ctx, adminprovision.ProvisionInput{
		Username:     b.cfg.Username,
		Email:        b.cfg.Username + "@gocell.local",
		PasswordHash: hash,
		RequireReset: true,
		Source:       domain.UserSourceBootstrap,
	})
	if err != nil {
		return ensureAdminResult{}, fmt.Errorf("initialadmin: ensure: %w", err)
	}
	switch result.Outcome {
	case adminprovision.OutcomeAlreadyExists, adminprovision.OutcomeRaceSkipped:
		// Concurrent replica / prior bootstrap won the race. Silent skip.
		return ensureAdminResult{}, nil
	case adminprovision.OutcomeCreated, adminprovision.OutcomeOrphanRecovered:
		// Fall through to credfile write.
	default:
		return ensureAdminResult{}, fmt.Errorf("initialadmin: unexpected provision outcome %d", result.Outcome)
	}

	// Write credential file. On failure, compensate the user/role writes so
	// the next startup can self-heal (adminExists==false again).
	cleaner, werr := b.writeFileAndMakeCleaner(password)
	if werr != nil {
		b.provisioner.Compensate(ctx, result.User.ID)
		return ensureAdminResult{}, werr
	}
	return ensureAdminResult{Cleaner: cleaner}, nil
}

// probeWriteable verifies the credential file directory is writable by creating
// and immediately removing a temporary probe file. Returns an error with an
// actionable message if the directory is not writable or cannot be created.
// On macOS, if the path starts with /run/, the error message includes a hint
// to set GOCELL_STATE_DIR=$TMPDIR/gocell (P2-11).
func (b *bootstrapper) probeWriteable() error {
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

// generateAndHash creates a random password and returns (plaintext, bcrypt hash).
// The intermediate byte slice is zeroed after hashing.
func (b *bootstrapper) generateAndHash() (password string, hash []byte, err error) {
	password, err = generatePassword(b.cfg.PasswordSource)
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

// writeFileAndMakeCleaner writes the credential file and constructs the cleanup worker.
// IMPORTANT: password is only referenced here and inside credentialPayload — it is not
// stored in any struct field and is not accessible after this function returns.
func (b *bootstrapper) writeFileAndMakeCleaner(password string) (worker.Worker, error) {
	now := b.deps.Clock.Now()
	expiresAt := now.Add(b.cfg.TTL)
	payload := credentialPayload{
		Username:    b.cfg.Username,
		Password:    password,
		ExpiresAt:   expiresAt,
		GeneratedAt: now,
	}
	if err := writeCredentialFile(b.cfg.CredentialPath, payload); err != nil {
		// IMPORTANT: do NOT include `password` in any log attribute below.
		// Caller (bootstrapper.ensureAdmin) runs compensateAfterCredFileFailure so the
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

	cleaner, err := newCleaner(cleanerConfig{
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
