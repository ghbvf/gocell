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
		case <-time.After(10 * time.Second):
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
	resp, err := http.Get("http://" + primaryAddr + "/healthz") //nolint:noctx
	require.NoError(t, err)
	resp.Body.Close()

	// Scrape /metrics from the HealthListener (B2: metrics are isolated on the
	// dedicated health port, not the primary listener).
	metricsResp, err := http.Get("http://" + healthAddr + "/metrics") //nolint:noctx
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
	assert.Contains(t, bodyStr, `cell="corebundle"`,
		"R2: /metrics output must have cell=\"corebundle\" label (proves auto-wire derives cell label from assembly ID). "+
			"Got /metrics body (first 400 chars): %s", truncateMetrics(bodyStr, 400))
}

// truncateMetrics returns at most n characters of s for use in assertion messages.
func truncateMetrics(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
