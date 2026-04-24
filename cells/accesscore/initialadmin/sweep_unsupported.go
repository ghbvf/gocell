//go:build !unix

package initialadmin

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/runtime/worker"
)

// sweepConfig parameterises startup-time credential sweep.
// On non-unix platforms, sweep is a no-op stub.
type sweepConfig struct {
	// StateDir is the directory to scan (typically $GOCELL_STATE_DIR).
	StateDir string
	// Clock supplies "now" for expiry comparison. nil → realClock{}.
	Clock Clock
	// Scheduler is used when constructing the returned cleaner worker. nil → realScheduler{}.
	Scheduler Scheduler
	// Logger is optional; nil falls back to slog.Default().
	Logger *slog.Logger
}

// sweep is a no-op on non-unix platforms.
// Credential file operations are only supported on unix.
func sweep(_ context.Context, _ sweepConfig) (worker.Worker, error) {
	return nil, nil
}
