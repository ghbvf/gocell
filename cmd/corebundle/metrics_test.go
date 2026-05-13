package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithMetricsTokenGuard(t *testing.T) {
	body := []byte("metrics-body")
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})

	cases := []struct {
		name           string
		configured     string
		submittedHdr   string // "" = header not set; non-empty value sent as X-Metrics-Token
		setHeader      bool
		wantStatus     int
		wantBodyPrefix string
	}{
		{
			name:           "matching token allows through",
			configured:     "secret-token",
			submittedHdr:   "secret-token",
			setHeader:      true,
			wantStatus:     http.StatusOK,
			wantBodyPrefix: "metrics-body",
		},
		{
			name:           "wrong token rejected",
			configured:     "secret-token",
			submittedHdr:   "wrong-token",
			setHeader:      true,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name:           "missing header rejected",
			configured:     "secret-token",
			setHeader:      false,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name:           "different length tokens rejected without leaking length",
			configured:     "long-configured-token",
			submittedHdr:   "x",
			setHeader:      true,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name: "empty configured + missing header — both hash to sha256(\"\"); allowed by " +
				"design (caller responsibility to fail-fast when token unset; see " +
				"buildMetricsHandler which logs warning and skips guard entirely)",
			configured:     "",
			setHeader:      false,
			wantStatus:     http.StatusOK,
			wantBodyPrefix: "metrics-body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := withMetricsTokenGuard(tc.configured, inner)
			req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
			if tc.setHeader {
				req.Header.Set(metricsAuthHeader, tc.submittedHdr)
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantBodyPrefix)
		})
	}
}

func TestBuildPromStack_Success(t *testing.T) {
	// buildPromStack uses an isolated registry (prom.NewRegistry, not the
	// global default), so this test does not need to clean up between runs.
	stack, err := buildPromStack()
	require.NoError(t, err)
	require.NotNil(t, stack.registry)
	require.NotNil(t, stack.hookObserver)
	require.NotNil(t, stack.metricProvider)
}

func TestBuildPromStack_ProducesIndependentRegistries(t *testing.T) {
	// Calling buildPromStack twice must yield isolated registries; otherwise
	// the second call would observe duplicate-collector errors when the
	// hookObserver re-registers its built-in collectors against the same
	// registry instance.
	first, err := buildPromStack()
	require.NoError(t, err)
	second, err := buildPromStack()
	require.NoError(t, err)
	assert.NotSame(t, first.registry, second.registry)
}

// Test-time durations extracted to package-level constants to satisfy
// archtest TEST-TIME-LITERAL-01 (extract test durations to consts).
const (
	// bootstrapWriteTimeoutSanity is the bootstrap HTTP WriteTimeout
	// (runtime/bootstrap/bootstrap_phase7.go defaultBootstrapHTTPWriteTimeout).
	// The metrics handler scrape timeout must be strictly less to ensure
	// promhttp can send a clean 503 before TCP write deadline kicks in.
	bootstrapWriteTimeoutSanity = 30 * time.Second
	// metricsTestScrapeTimeout is a tiny test-only timeout to avoid waiting
	// the production 10s value in unit tests.
	metricsTestScrapeTimeout = 50 * time.Millisecond
	// metricsTestSlowGatherMultiplier — slow Gather sleeps this many times
	// the test scrape timeout, guaranteeing the timeout path trips.
	metricsTestSlowGatherMultiplier = 5
	// metricsInflightTestGatherTimeout — long enough that
	// promhttp.Timeout never fires before the test exits, isolating the
	// MaxRequestsInFlight 503 path.
	metricsInflightTestGatherTimeout = 5 * time.Second
	// metricsInflightSettleDelay — short pause to let saturating goroutines
	// enter Gather() and decrement the inflight semaphore before the test
	// fires its limit-trip request.
	metricsInflightSettleDelay = 20 * time.Millisecond
)

// slowGatherer is a prom.Gatherer that blocks for the configured duration on
// every Gather() call. Used to exercise promhttp.HandlerFor's Timeout path.
type slowGatherer struct {
	sleep time.Duration
	inner *prom.Registry
}

func (g *slowGatherer) Gather() ([]*dto.MetricFamily, error) {
	time.Sleep(g.sleep) //archtest:allow:test-sleep slowGatherer simulates upstream Gather latency for promhttp Timeout 503 path
	return g.inner.Gather()
}

// TestBuildMetricsHandler_TimeoutReturns503 verifies that a Gather() call
// blocking longer than metricsScrapeTimeout (10s) is terminated by
// promhttp.HandlerFor's Timeout path, returning HTTP 503 + the
// "Exceeded configured timeout of {dur}." body. The handler-level timeout
// must fire before the bootstrap-level WriteTimeout (30s) so the client
// gets a clean response rather than a TCP RST.
//
// We construct the handler directly via promhttp.HandlerFor (mirroring
// buildMetricsHandler's wiring) with a small Timeout to keep the test fast.
func TestBuildMetricsHandler_TimeoutReturns503(t *testing.T) {
	t.Parallel()

	// Sanity: the production constant must be < bootstrap WriteTimeout.
	require.Less(t, metricsScrapeTimeout, bootstrapWriteTimeoutSanity,
		"metricsScrapeTimeout must be strictly less than bootstrap HTTP WriteTimeout")

	gatherer := &slowGatherer{
		sleep: metricsTestSlowGatherMultiplier * metricsTestScrapeTimeout,
		inner: prom.NewRegistry(),
	}

	h := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		Timeout:             metricsTestScrapeTimeout,
		MaxRequestsInFlight: metricsMaxRequestsInFlight,
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Exceeded configured timeout") {
		t.Fatalf("body does not contain promhttp timeout text: %q", rec.Body.String())
	}
}

// TestBuildMetricsHandler_MaxRequestsInFlight verifies that requests
// exceeding metricsMaxRequestsInFlight receive 503. We hold the configured
// limit of slow Gather calls in flight and confirm the next request returns
// 503 immediately.
func TestBuildMetricsHandler_MaxRequestsInFlight(t *testing.T) {
	t.Parallel()

	// Each inflight Gather blocks until release is closed.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	gatherer := prom.GathererFunc(func() ([]*dto.MetricFamily, error) {
		<-release
		return nil, nil
	})

	h := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		Timeout:             metricsInflightTestGatherTimeout, // longer than test runtime
		MaxRequestsInFlight: metricsMaxRequestsInFlight,
	})

	// Saturate the inflight budget.
	var saturated sync.WaitGroup
	saturated.Add(metricsMaxRequestsInFlight)
	for i := 0; i < metricsMaxRequestsInFlight; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			rec := httptest.NewRecorder()
			saturated.Done()
			h.ServeHTTP(rec, req)
		}()
	}
	saturated.Wait()
	// Give the goroutines a moment to enter Gather() and decrement the
	// inflight semaphore. promhttp's limiter is preemptive — without a
	// small sync delay the limit check can race against goroutine start.
	time.Sleep(metricsInflightSettleDelay) //archtest:allow:test-sleep inflight semaphore decrement timing

	// One more request must trip the 503 path.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (inflight limit not enforced); body=%q",
			rec.Code, rec.Body.String())
	}
}
