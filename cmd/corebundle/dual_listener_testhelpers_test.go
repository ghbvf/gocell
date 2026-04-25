package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newCorebundleLocalListener returns an ephemeral TCP listener bound to
// 127.0.0.1:0 for tests that need to inject an internal listener alongside
// a primary test listener. Mirrors bootstrap_test.newLocalListener but lives
// in the corebundle package to avoid a bootstrap test-helper export.
func newCorebundleLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create test listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// waitForHealthy polls /healthz on addr until it returns 200 or the deadline
// expires. addr must be a host:port string (no scheme). The path stays
// /healthz regardless of which listener serves it: in production it lives on
// the HealthListener, but the phase5 fallback (test convenience) remaps it
// onto the PrimaryListener when no HealthListener is declared. Tests that
// probe /healthz on either port should call this helper instead of
// hand-rolled http.Get retries (R2-02). The retry loop also covers the
// startup race where the listener has bound but Bootstrap.Run has not yet
// attached the chi.Mux.
func waitForHealthy(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "HTTP /healthz did not become ready on %s", addr)
}
