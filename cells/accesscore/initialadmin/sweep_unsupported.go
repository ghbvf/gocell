//go:build !unix && !windows

package initialadmin

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/clock"
)

// sweepConfig parameterises startup-time credential sweep.
// On non-unix platforms, sweep is a no-op stub.
type sweepConfig struct {
	// CredentialPath is the exact credential file to sweep.
	CredentialPath string
	// Clock supplies "now" for expiry comparison. nil → clock.Real().
	Clock clock.Clock
	// Scheduler is used when constructing the returned cleaner worker. nil → realScheduler{}.
	Scheduler Scheduler
	// Logger is optional; nil falls back to slog.Default().
	Logger *slog.Logger
}

// sweep is a no-op on non-unix platforms.
// Credential file operations are only supported on unix.
func sweep(_ context.Context, _ sweepConfig) (sweepResult, error) {
	return sweepResult{}, nil
}
