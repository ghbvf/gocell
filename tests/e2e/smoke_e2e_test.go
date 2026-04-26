//go:build e2e

// Package e2e holds the unconditional smoke tests that drive the e2egate
// "at least one test executed" signal. Capability tests live in sibling
// packages (e.g., tests/e2e/encryption) so capability gating emerges
// from e2egate's per-package "all-skipped" rule rather than from new
// gate plumbing — see tests/e2e/encryption/doc.go for the full model.
//
// These tests run against the demo+memory docker-compose harness
// (tests/e2e/docker-compose.e2e.yaml). See PR-CFG-G G.8 for the real-PG
// harness rebuild that unskips the encryption capability tests.
package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tests/e2e/internal/clients"
	e2erequire "github.com/ghbvf/gocell/tests/e2e/internal/require"
)

// TestE2E_HealthListener_ReadyzReturns200 is the unconditional smoke test
// the e2egate parser uses as its at-least-one-executed signal. It runs in
// every environment that has docker available (no PG / RMQ dependency),
// so the gate stays meaningful even when the encryption capability tests
// are gated out by the build tag.
func TestE2E_HealthListener_ReadyzReturns200(t *testing.T) {
	e2erequire.Docker(t)
	clients.WaitForReady(t, 30*time.Second)

	resp, err := http.Get(clients.HealthURL() + "/readyz") //nolint:noctx
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestE2E_PrimaryListener_RejectsInternalPath verifies the dual-listener
// physical isolation guarantee: /internal/v1/* must 404 on the primary
// listener (PR-A14b). Like the readyz smoke, this runs without PG/RMQ.
func TestE2E_PrimaryListener_RejectsInternalPath(t *testing.T) {
	e2erequire.Docker(t)
	clients.WaitForReady(t, 30*time.Second)

	resp, err := http.Get(clients.BaseURL() + "/internal/v1/anything") //nolint:noctx
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"primary listener must 404 internal-prefixed paths (PR-A14b dual-listener isolation)")
}
