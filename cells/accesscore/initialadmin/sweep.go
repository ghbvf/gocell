package initialadmin

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/worker"
)

// sweepConfig parameterises startup-time credential sweep.
type sweepConfig struct {
	// StateDir is the directory to scan (typically $GOCELL_STATE_DIR).
	// An empty string falls back to ResolveCredentialPath("") semantics
	// (GOCELL_STATE_DIR env var → platform default credential path).
	StateDir string
	// Clock supplies "now" for expiry comparison. nil → realClock{}.
	Clock Clock
	// Scheduler is used when constructing the returned cleaner worker. nil → realScheduler{}.
	Scheduler Scheduler
	// Logger is optional; nil falls back to slog.Default().
	Logger *slog.Logger
}

// sweep performs a startup-time unconditional credential sweep, independent
// of adminExists. This closes the P1-16 gap: when adminExists==true,
// ensureAdmin returns early without cleaning orphan cred files; sweep always
// runs and removes them if they have expired.
//
// Algorithm:
//  1. Resolve the credential file path (ResolveCredentialPath or StateDir).
//  2. File absent → no-op, return (nil, nil).
//  3. Read expires_at. Parse failure → slog.Error + return (nil, nil) (file
//     retained; never delete unknown formats to guard against false positives).
//  4. expires_at <= now → removeCredentialFile + slog.Info, return (nil, nil).
//  5. Not yet expired → construct and return a cleaner worker so the caller
//     can register it for runtime TTL cleanup (closes P1-16 runtime window).
//
// The returned worker is non-nil only in case 5 (fresh orphan file). The caller
// is responsible for wiring the returned worker (typically via the existing lazy
// bootstrap-worker sink).
//
// sweep never blocks startup: non-ENOENT FS errors are logged at Error and
// the function returns (nil, nil) so the caller can proceed. The only exception
// is a misconfigured StateDir (non-absolute path), which is a programmer error
// surfaced as a returned error so it fails fast.
//
// Conflict note: when adminExists==false AND a fresh orphan file exists, sweep
// retains the file and returns a cleaner, but ensureAdmin will subsequently
// attempt to write a new credential file and fail with errCredFileExists. This
// is a rare bootstrap-interruption scenario not covered by P1-16; it surfaces
// as an ensureAdmin error and requires operator intervention.
func sweep(ctx context.Context, cfg sweepConfig) (worker.Worker, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	clk := cfg.Clock
	if clk == nil {
		clk = realClock{}
	}

	credPath, err := ResolveCredentialPath(cfg.StateDir)
	if err != nil {
		// Non-absolute StateDir is a configuration error — fail fast.
		return nil, err
	}

	expiresAt, err := readCredentialExpiresAt(credPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No file — nothing to sweep.
			return nil, nil
		}
		// Unreadable file or parse error: log and continue startup (don't delete).
		// Attach failure_kind so operators can distinguish permission errors from
		// other IO errors without changing the returned errcode.
		var failureKind string
		if errors.Is(err, fs.ErrPermission) {
			failureKind = "permission"
		} else {
			failureKind = "io"
		}
		cfg.Logger.ErrorContext(ctx, "sweep: cannot read credential file; retaining",
			slog.String("event", "initial_admin_credential_sweep_error"),
			slog.String("file_path", credPath),
			slog.String("failure_kind", failureKind),
			slog.Any("error", errcode.WrapInfra(errcode.ErrInternal, "sweep: read cred file", err)),
		)
		return nil, nil
	}

	now := clk.Now()
	if !expiresAt.After(now) {
		// Expired — remove.
		if removeErr := removeCredentialFile(credPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cfg.Logger.ErrorContext(ctx, "sweep: failed to remove expired credential file",
				slog.String("event", "initial_admin_credential_sweep_error"),
				slog.String("file_path", credPath),
				slog.Any("error", errcode.WrapInfra(errcode.ErrInternal, "sweep: remove cred file", removeErr)),
			)
			return nil, nil
		}
		cfg.Logger.InfoContext(ctx, "sweep: removed expired credential file",
			slog.String("event", "initial_admin_credential_swept"),
			slog.String("file_path", credPath),
			slog.Time("expires_at", expiresAt),
		)
		return nil, nil
	}

	// Not expired — re-register a cleaner worker so the runtime cleans up after
	// the remaining TTL elapses (closes P1-16 runtime window for fresh orphan files).
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		// Should not happen (checked above), but guard for safety.
		remaining = time.Nanosecond
	}
	cfg.Logger.InfoContext(ctx, "sweep: credential file not expired, cleaner re-registered",
		slog.String("event", "initial_admin_credential_sweep_cleaner"),
		slog.String("file_path", credPath),
		slog.Time("expires_at", expiresAt),
		slog.Duration("remaining", remaining),
	)
	cleaner, err := newCleaner(cleanerConfig{
		Path:      credPath,
		TTL:       remaining,
		Clock:     clk,
		Scheduler: cfg.Scheduler,
		Logger:    cfg.Logger,
	})
	if err != nil {
		// newCleaner should not fail with valid path/TTL — treat as infra error.
		cfg.Logger.ErrorContext(ctx, "sweep: failed to construct cleaner for fresh orphan file",
			slog.String("event", "initial_admin_credential_sweep_error"),
			slog.String("file_path", credPath),
			slog.Any("error", err),
		)
		return nil, nil
	}
	return cleaner, nil
}
