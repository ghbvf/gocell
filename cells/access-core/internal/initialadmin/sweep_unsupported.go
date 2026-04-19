//go:build !unix

package initialadmin

import (
	"log/slog"

	bootstraprt "github.com/ghbvf/gocell/runtime/bootstrap"
)

// SweepConfig parameterises startup-time credential sweep.
// On non-unix platforms, sweep is a no-op stub.
type SweepConfig struct {
	// StateDir is the directory to scan (typically $GOCELL_STATE_DIR).
	StateDir string
	// Clock supplies "now" for expiry comparison. nil → RealClock{}.
	Clock Clock
	// Logger is required.
	Logger *slog.Logger
}

// SweepHook returns a no-op bootstrap.Hook on non-unix platforms.
// Credential file operations are only supported on unix.
func SweepHook(_ SweepConfig) bootstraprt.Hook {
	return bootstraprt.Hook{
		Name:    "initialadmin.sweep",
		OnStart: nil,
		OnStop:  nil,
	}
}
