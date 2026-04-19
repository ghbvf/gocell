//go:build !unix

package initialadmin

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/runtime/worker"
)

// SweepConfig parameterises startup-time credential sweep.
// On non-unix platforms, sweep is a no-op stub.
type SweepConfig struct {
	// StateDir is the directory to scan (typically $GOCELL_STATE_DIR).
	StateDir string
	// Clock supplies "now" for expiry comparison. nil → RealClock{}.
	Clock Clock
	// Scheduler is used when constructing the returned Cleaner worker. nil → RealScheduler{}.
	Scheduler Scheduler
	// Logger is optional; nil falls back to slog.Default().
	Logger *slog.Logger
}

// Sweep is a no-op on non-unix platforms.
// Credential file operations are only supported on unix.
func Sweep(_ context.Context, _ SweepConfig) (worker.Worker, error) {
	return nil, nil
}
