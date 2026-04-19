//go:build unix

package initialadmin

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	bootstraprt "github.com/ghbvf/gocell/runtime/bootstrap"
)

// sweepStartTimeout is the default OnStart deadline for SweepHook.
// FS operations are sub-millisecond; 5s absorbs kernel stalls without
// blocking startup beyond acceptable limits.
const sweepStartTimeout = 5 * time.Second

// SweepConfig parameterises startup-time credential sweep.
type SweepConfig struct {
	// StateDir is the directory to scan (typically $GOCELL_STATE_DIR).
	// An empty string falls back to ResolveCredentialPath("") semantics
	// (GOCELL_STATE_DIR env var → /run/gocell/initial_admin_password).
	StateDir string
	// Clock supplies "now" for expiry comparison. nil → RealClock{}.
	Clock Clock
	// Logger is required.
	Logger *slog.Logger
}

// Sweep performs a startup-time unconditional credential sweep, independent
// of adminExists. This closes the P1-16 gap: when adminExists==true,
// EnsureAdmin returns early without cleaning orphan cred files; Sweep always
// runs and removes them if they have expired.
//
// Algorithm:
//  1. Resolve the credential file path (ResolveCredentialPath or StateDir).
//  2. File absent → no-op, return nil.
//  3. Read expires_at. Parse failure → slog.Error + return nil (file retained;
//     never delete unknown formats to guard against false positives).
//  4. expires_at <= now → RemoveCredentialFile + slog.Warn.
//  5. Not yet expired → retain (Cleaner worker handles runtime TTL).
//
// Sweep never blocks startup: non-ENOENT FS errors are logged at Error and
// the function returns nil so the caller can proceed. The only exception is
// a misconfigured StateDir (non-absolute path), which is a programmer error
// surfaced as a returned error so it fails fast.
func Sweep(ctx context.Context, cfg SweepConfig) error {
	clk := cfg.Clock
	if clk == nil {
		clk = RealClock{}
	}

	credPath, err := ResolveCredentialPath(cfg.StateDir)
	if err != nil {
		// Non-absolute StateDir is a configuration error — fail fast.
		return err
	}

	expiresAt, err := ReadCredentialExpiresAt(credPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No file — nothing to sweep.
			return nil
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
		return nil
	}

	now := clk.Now()
	if !expiresAt.After(now) {
		// Expired — remove.
		if removeErr := RemoveCredentialFile(credPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cfg.Logger.ErrorContext(ctx, "sweep: failed to remove expired credential file",
				slog.String("event", "initial_admin_credential_sweep_error"),
				slog.String("file_path", credPath),
				slog.Any("error", errcode.WrapInfra(errcode.ErrInternal, "sweep: remove cred file", removeErr)),
			)
			return nil
		}
		cfg.Logger.InfoContext(ctx, "sweep: removed expired credential file",
			slog.String("event", "initial_admin_credential_swept"),
			slog.String("file_path", credPath),
			slog.Time("expires_at", expiresAt),
		)
		return nil
	}

	// Not expired — retain; Cleaner worker handles TTL at runtime.
	return nil
}

// SweepHook returns a bootstrap.Hook whose OnStart executes Sweep with the
// supplied cfg. OnStop is nil (sweep is a one-shot startup action).
// StartTimeout is set to sweepStartTimeout (5s).
func SweepHook(cfg SweepConfig) bootstraprt.Hook {
	return bootstraprt.Hook{
		Name: "initialadmin.sweep",
		OnStart: func(ctx context.Context) error {
			return Sweep(ctx, cfg)
		},
		OnStop:       nil,
		StartTimeout: sweepStartTimeout,
	}
}
