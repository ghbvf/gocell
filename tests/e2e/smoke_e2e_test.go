//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	e2erequire "github.com/ghbvf/gocell/tests/e2e/internal/require"
)

// TestE2E_HealthListener_ReadyzReturns200 is the unconditional smoke test
// the e2egate parser uses as its at-least-one-executed signal. It runs in
// every environment that has docker available (no PG / RMQ dependency), so
// the gate stays meaningful even when the encryption-specific tests are
// skipped via require.PG.
func TestE2E_HealthListener_ReadyzReturns200(t *testing.T) {
	e2erequire.Docker(t)
	waitForReady(t, 30*time.Second)

	resp, err := http.Get(e2eHealthURL() + "/readyz") //nolint:noctx
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestE2E_PrimaryListener_RejectsInternalPath verifies the dual-listener
// physical isolation guarantee: /internal/v1/* must 404 on the primary
// listener (PR-A14b). Like the readyz smoke, this runs without PG/RMQ.
func TestE2E_PrimaryListener_RejectsInternalPath(t *testing.T) {
	e2erequire.Docker(t)
	waitForReady(t, 30*time.Second)

	resp, err := http.Get(e2eBaseURL() + "/internal/v1/anything") //nolint:noctx
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"primary listener must 404 internal-prefixed paths (PR-A14b dual-listener isolation)")
}
