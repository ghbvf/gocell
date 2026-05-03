//go:build integration

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestR2_MetricsCollector_RecordsHTTPRequests is the R2 wiring integration test.
//
// Goal: verify the end-to-end path from
//
//	bootstrap.WithMetricsProvider → autoWireHTTPMetricsCollector → router.WithMetricsCollector
//	→ middleware.Metrics → RecordRequest
//	→ /metrics Prometheus text output contains http_requests_total
//
// Approach: build a full bootstrap with memory topology (same as
// TestBuildBootstrap_MemoryTopology), fire one HTTP request that traverses the
// outerMux (so Metrics middleware fires), then scrape /metrics and assert the
// Prometheus text contains "http_requests_total{".
//
// Note: this is not a full observability-pipeline e2e test. It verifies the
// auto-wire wiring chain is correct end-to-end at the HTTP level. Full
// scrape-pipeline e2e (with a live Prometheus scraper) is deferred to a
// follow-up once the observability stack has a dedicated integration harness.
func TestR2_MetricsCollector_RecordsHTTPRequests(t *testing.T) {
	shared := buildTestSharedDeps(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(t, shared, ln,
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, bootstrap.WithListenerNet(healthLn)))
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(testtime.SelectAsyncSettle):
			t.Error("bootstrap did not shut down in time")
		}
	})

	primaryAddr := ln.Addr().String()
	healthAddr := healthLn.Addr().String()

	// Wait until the health listener is healthy before firing measurement requests.
	waitForHealthy(t, healthAddr)

	// Fire a request to the primary listener — this traverses outerMux which
	// includes the Metrics middleware wired by autoWireHTTPMetricsCollector. The
	// request is counted against the Prometheus registry backing the auto-wired
	// collector. Use /healthz on primary (fallback: not found, still traverses
	// outerMux) — any path will trigger the Metrics middleware.
	resp, err := http.Get("http://" + primaryAddr + "/healthz")
	require.NoError(t, err)
	resp.Body.Close()

	// Scrape /metrics from the HealthListener (B2: metrics are isolated on the
	// dedicated health port, not the primary listener).
	metricsResp, err := http.Get("http://" + healthAddr + "/metrics")
	require.NoError(t, err)
	defer metricsResp.Body.Close()
	require.Equal(t, http.StatusOK, metricsResp.StatusCode,
		"/metrics endpoint must be reachable (R2 wiring check)")

	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)

	bodyStr := string(body)
	assert.True(t, strings.Contains(bodyStr, "http_requests_total{"),
		"R2: /metrics output must contain http_requests_total{ after at least one request. "+
			"This verifies the full wiring chain: WithMetricsProvider → autoWireHTTPMetricsCollector "+
			"→ router.WithMetricsCollector → middleware.Metrics → RecordRequest → Prometheus registry. "+
			"Got /metrics body (first 400 chars): %s", truncateMetrics(bodyStr, 400))
	// HTTP-METRICS-LABEL-REALIGN: requests that do not match any cell-owned
	// RouteGroup must be labelled with the framework "_runtime" sentinel
	// (installed by router.go), not with the assembly ID. The /healthz hit on
	// PrimaryListener does not match any RouteGroup (HealthListener carries
	// /healthz on its own router), so it falls through to the sentinel layer.
	assert.Contains(t, bodyStr, `cell="_runtime"`,
		"R2: /metrics output must label framework-owned requests cell=\"_runtime\" "+
			"(installed by router.go on every listener-root mux). "+
			"Got /metrics body (first 400 chars): %s", truncateMetrics(bodyStr, 400))
	assert.NotContains(t, bodyStr, `cell="corebundle"`,
		"R2: post HTTP-METRICS-LABEL-REALIGN, the assembly ID must not leak into the cell label. "+
			"A regression that re-derives cellID from b.assemblyID would fail here. "+
			"Got /metrics body (first 400 chars): %s", truncateMetrics(bodyStr, 400))
}

// truncateMetrics returns at most n characters of s for use in assertion messages.
func truncateMetrics(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestR2_MetricsTokenGuard_ListenerWiring locks the cross-route security
// contract for /metrics under a configured GOCELL_METRICS_TOKEN. The unit-
// level helper test (cmd/corebundle/metrics_test.go::TestWithMetricsTokenGuard)
// proves the guard logic itself; this integration test proves the *wiring* —
// that buildMetricsHandler actually wraps /metrics with the guard, that the
// guard scope is /metrics only (not the whole listener mux), and that the
// guard rejects every wrong/missing-token shape.
//
// Cases (all on the same bootstrap to amortize startup cost):
//  1. token configured, no header on /metrics            → 401
//  2. token configured, wrong header value on /metrics   → 401
//  3. token configured, correct header on /metrics       → 200
//  4. token configured, no header on /healthz (cross-route guard scope) → 200
//
// Case 4 is the critical "guard didn't accidentally promote to listener-level
// middleware" check — kubelet probes hit /healthz without the metrics token
// and must keep working. A regression that wrapped the listener mux instead
// of the /metrics handler alone would break liveness probes; this test
// surfaces that immediately.
//
// Baseline (token unset, anonymous /metrics → 200) is covered by
// TestR2_MetricsCollector_RecordsHTTPRequests above.
func TestR2_MetricsTokenGuard_ListenerWiring(t *testing.T) {
	const token = "test-metrics-token-r2"

	shared := buildTestSharedDeps(t)
	shared.MetricsToken = token // inject token so buildMetricsHandler wraps with guard

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	app, err := buildBootstrapFromShared(t, shared, ln,
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, bootstrap.WithListenerNet(healthLn)))
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(testtime.SelectAsyncSettle):
			t.Error("bootstrap did not shut down in time")
		}
	})

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

	cases := []struct {
		name       string
		path       string
		header     string // empty = don't set X-Metrics-Token
		wantStatus int
	}{
		{"metrics_no_header_401", "/metrics", "", http.StatusUnauthorized},
		{"metrics_wrong_header_401", "/metrics", "wrong-token-value", http.StatusUnauthorized},
		{"metrics_correct_header_200", "/metrics", token, http.StatusOK},
		// Guard scope check: /healthz must remain reachable without the
		// metrics token, otherwise kubelet probes break.
		{"healthz_no_header_still_200", "/healthz", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+healthAddr+tc.path, http.NoBody)
			require.NoError(t, err)
			if tc.header != "" {
				req.Header.Set("X-Metrics-Token", tc.header)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, tc.wantStatus, resp.StatusCode,
				"path=%s header=%q body=%s", tc.path, tc.header, truncateMetrics(string(body), 200))
		})
	}
}
