package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

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

	app, err := buildBootstrapFromShared(t, shared, bootstrap.WithPrimaryListener(ln), bootstrap.WithInternalListener(newCorebundleLocalListener(t)))
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

	addr := ln.Addr().String()

	// Wait until the server is healthy before firing measurement requests.
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "bootstrap must become healthy before R2 assertions")

	// Fire a request to /healthz — this traverses outerMux which includes the
	// Metrics middleware wired by autoWireHTTPMetricsCollector. The request is
	// counted against the Prometheus registry backing the auto-wired collector.
	resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
	require.NoError(t, err)
	resp.Body.Close()

	// Scrape /metrics and verify http_requests_total is present.
	metricsResp, err := http.Get("http://" + addr + "/metrics") //nolint:noctx
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
