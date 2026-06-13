package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/transport"
)

// TestPhase5BindInProcessTransport_BindsFinalizedAuthChain is the phase5 wiring
// integration test (PR #2092 F6): it builds the internal-listener Router with the
// REAL service-token middleware (as phase5 does via applyListenerAuthChain),
// mounts a stub configcore route, finalizes auth, then drives
// phase5BindInProcessTransport. It proves the bound handler is the FINALIZED
// internal router (auth chain compiled), so an in-process DoContract runs the
// same ServiceTokenMiddleware a network request would — a signed token reaches
// the handler (200), an unsigned request is rejected (401) before it.
func TestPhase5BindInProcessTransport_BindsFinalizedAuthChain(t *testing.T) {
	clk := clock.Real()
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	require.NoError(t, err)
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	require.NoError(t, err)

	var reached bool
	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	rtr, err := router.NewForListener(clk, cell.InternalListener,
		router.WithDefaultMiddleware(auth.ServiceTokenMiddleware(ring, clk, auth.WithServiceTokenNonceStore(ns))),
		// The stub route is a synthetic placeholder (no auth.Mount metadata); this
		// test exercises the LISTENER auth chain (service-token middleware) bound by
		// phase5, not route-level policy coverage, so exempt it from coverage.
		router.WithPolicyCoverageWhitelist([]string{"/internal/v1/config/*"}))
	require.NoError(t, err)
	rtr.Handle("/internal/v1/config/", stub)
	require.NoError(t, rtr.FinalizeAuth())

	holder := transport.NewInProcess(nil)
	b := New(clk)
	b.inProcessTransport = holder
	require.NoError(t, b.phase5BindInProcessTransport(
		map[cell.ListenerRef]*router.Router{cell.InternalListener: rtr}),
		"phase5 must bind the finalized internal-listener handler")

	tid, err := tenant.ParseTenantID("f47ac10b-58cc-4372-a567-0e02b2c3d479")
	require.NoError(t, err)
	const path = "/internal/v1/config/app.name"

	t.Run("signed request reaches the handler", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		token := auth.GenerateServiceToken(ring, "accesscore", http.MethodGet, path, "", tid, clk.Now())
		req.Header.Set("Authorization", "ServiceToken "+token)
		req.Header.Set(auth.HeaderTenantID, tid.String())

		resp, err := holder.DoContract(context.Background(), "http.config.internal.get.v1", req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "signed token must pass the finalized auth chain")
		assert.True(t, reached, "handler must be reached for a signed in-process request")
	})

	t.Run("unsigned request rejected before the handler", func(t *testing.T) {
		reached = false
		resp, err := holder.DoContract(context.Background(), "http.config.internal.get.v1",
			httptest.NewRequest(http.MethodGet, path, nil))
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"unsigned request must be rejected — phase5-bound in-proc path must not bypass auth")
		assert.False(t, reached, "handler must not be reached for an unsigned request")
	})
}

// TestPhase5BindInProcessTransport_NoInternalListener_LeavesUnbound asserts that
// when no InternalListener is declared, phase5 leaves the holder unbound (no
// error), and its DoContract then fail-fasts rather than dispatching to nil.
func TestPhase5BindInProcessTransport_NoInternalListener_LeavesUnbound(t *testing.T) {
	holder := transport.NewInProcess(nil)
	b := New(clock.Real())
	b.inProcessTransport = holder

	require.NoError(t, b.phase5BindInProcessTransport(map[cell.ListenerRef]*router.Router{}),
		"no InternalListener → no error, holder left unbound")

	//nolint:bodyclose // the unbound fail-fast path returns a nil response (asserted via err); no body to close.
	_, err := holder.DoContract(context.Background(), "c",
		httptest.NewRequest(http.MethodGet, "/internal/v1/config/x", nil))
	require.Error(t, err, "DoContract on an unbound holder must fail-fast, not nil-panic")
}
