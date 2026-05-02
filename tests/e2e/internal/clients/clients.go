//go:build e2e

// Package clients exposes shared HTTP helpers and wait utilities for the
// gocell e2e suite. Lives under internal/ so only e2e test packages
// (tests/e2e and its sub-packages such as tests/e2e/encryption) may
// import it.
//
// The build tag keeps the package out of non-e2e compilations entirely;
// there is no shadow doc.go without the tag because no caller exists
// outside of e2e test files.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// e2eClock backs WaitForReady's polling loop. It is the singleton injected
// via [clock.Real]; e2e tests run against real services so wall-clock time
// is the only time source that makes sense here.
var e2eClock = clock.Real()

// defaultE2ERetryInterval is the WaitForReady poll cadence; small enough that
// startup latency is dominated by the server's own readiness signal.
const defaultE2ERetryInterval = 500 * time.Millisecond

// BaseURL returns the primary listener (business API) base URL, defaulting
// to localhost:8080. Override via E2E_BASE_URL.
func BaseURL() string {
	if u := os.Getenv("E2E_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// HealthURL returns the health listener base URL — /healthz, /readyz and
// /metrics live here, NOT on the primary listener (PR-A14b dual-listener
// isolation). Defaults to localhost:9091; override via E2E_HEALTH_URL.
func HealthURL() string {
	if u := os.Getenv("E2E_HEALTH_URL"); u != "" {
		return u
	}
	return "http://localhost:9091"
}

// AdminToken returns the admin JWT minted by the bootstrap-admin step.
// Empty token is fail-fast: capability tests that expect a token must
// abort immediately rather than send anonymous requests that "happen to
// work" against unauth endpoints — that pattern silently masks auth
// regressions.
func AdminToken(t *testing.T) string {
	t.Helper()
	token := os.Getenv("E2E_ADMIN_TOKEN")
	require.NotEmpty(t, token,
		"E2E_ADMIN_TOKEN must be set; the e2e workflow's bootstrap-admin step injects it via $GITHUB_ENV")
	return token
}

// WaitForReady polls the health listener's /readyz until it returns 200
// or the timeout elapses. /readyz is strictly stronger than /healthz —
// it waits for cell readiness (DB pools, migrations, outbox dispatchers).
func WaitForReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := e2eClock.Now().Add(timeout)
	for e2eClock.Now().Before(deadline) {
		resp, err := http.Get(HealthURL() + "/readyz")
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		_ = e2eClock.Sleep(context.Background(), e2eClock.Now().Add(defaultE2ERetryInterval))
	}
	t.Fatalf("server at %s did not become ready within %s", HealthURL(), timeout)
}

// DoJSON sends a JSON request to the primary listener; pass token="" for
// an explicitly anonymous call (used by negative auth tests). The
// returned response body is the caller's responsibility to close.
func DoJSON(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var reqBody bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&reqBody).Encode(body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, BaseURL()+path, &reqBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
