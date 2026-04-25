// Package require provides conditional-skip helpers for e2e tests.
// Each helper checks whether a required infrastructure is available
// (via env var or runtime probe) and skips the test if absent — never
// fails. Wraps the analyzer-required if-conditional pattern.
package require

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil"
)

// Docker skips the test if Docker is not available. Detection probes the
// DOCKER_HOST env var first, then the default /var/run/docker.sock unix
// socket (1-second timeout). Set GOCELL_E2E_DOCKER_AVAILABLE=1 in CI to
// bypass the probe and trust that docker compose is already running.
//
// Analyzer false-positive risk: the skip call inside testutil.RequireDocker is
// already guarded by an if-conditional (if dockerAvailable()), so it is never
// an unconditional first statement at the call site. The unconditionalskip
// analyzer will not flag this function.
func Docker(t *testing.T) {
	t.Helper()
	if os.Getenv("GOCELL_E2E_DOCKER_AVAILABLE") == "1" {
		return
	}
	testutil.RequireDocker(t)
}
