// metrics.go: Prometheus 指标栈构建与 /metrics handler 守卫。
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	adapterprom "github.com/ghbvf/gocell/adapters/prometheus"
)

const (
	// metricsAuthHeader names the request header used to authenticate
	// /metrics scrapers when a bearer token is configured. Mirrors the
	// X-Readyz-Token convention for /readyz?verbose — keeping the same shape
	// for all control-plane endpoints lets operators standardize scraper config.
	metricsAuthHeader = "X-Metrics-Token"

	// metricsScrapeTimeout is the application-level Gather() timeout for the
	// /metrics handler. Set to 10s following the Prometheus official default
	// scrape_timeout. Must be strictly less than the bootstrap HTTP
	// WriteTimeout (30s, runtime/bootstrap/bootstrap_phase7.go) so promhttp
	// can send a clean 503 + "Exceeded configured timeout of {dur}.\n" before
	// the TCP write deadline kicks in and forcibly closes the connection.
	//
	// Caveat: promhttp's Timeout option implements scrape budget enforcement
	// by spawning Gather() in a goroutine and racing it against a time.After
	// channel — the prom.Gatherer interface carries no context, so an
	// overrunning Gather call keeps executing in the background even after
	// the 503 has been written. All current GoCell collectors are pure
	// in-memory reads (CounterVec/HistogramVec.Collect), so a stranded
	// Gather goroutine is benign; introducing an IO collector later requires
	// either (a) a custom Gatherer that owns its own context-cancellable
	// reads, or (b) upstream context plumbing in client_golang.
	//
	// ref: prometheus/client_golang prometheus/promhttp/http.go HandlerOpts.Timeout
	metricsScrapeTimeout = 10 * time.Second

	// metricsMaxRequestsInFlight caps concurrent /metrics scrapes. Normal
	// operation has a single Prometheus server scraping (concurrency 1); HA
	// dual-Prometheus + an operator running curl tops out at 3. Excess
	// requests get 503 so the scraper backs off, preventing a slow-scrape
	// queue from exhausting HealthListener goroutines. Ordering: the outer
	// withMetricsTokenGuard handles 401 before this limit checks 503, so
	// unauthenticated traffic cannot exhaust the inflight budget (nginx
	// auth_request → limit_req parity).
	metricsMaxRequestsInFlight = 3
)

// withMetricsTokenGuard wraps h so requests without a matching
// X-Metrics-Token header are rejected with 401 Unauthorized.
//
// PR-258 RES-6: compares SHA-256 digests rather than the raw byte slices so
// a caller cannot learn the configured token length via timing. subtle.
// ConstantTimeCompare short-circuits on length mismatch, which turns the
// bare-bytes form into a length oracle even though byte comparison itself
// is constant-time. Hashing both sides normalises to a fixed 32 bytes
// regardless of input length — same model as runtime/http/health.go's
// /readyz?verbose token gate.
func withMetricsTokenGuard(token string, h http.Handler) http.Handler {
	configured := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitted := sha256.Sum256([]byte(r.Header.Get(metricsAuthHeader)))
		if subtle.ConstantTimeCompare(submitted[:], configured[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// promStack groups the Prometheus hook observer and metric provider.
type promStack struct {
	registry       *prom.Registry
	hookObserver   *adapterprom.HookObserver
	metricProvider *adapterprom.MetricProvider
}

// buildPromStack creates an isolated Prometheus registry, a hook observer,
// and a metric provider on top of it.
func buildPromStack() (promStack, error) {
	registry := prom.NewRegistry()
	hookObserver, err := adapterprom.NewHookObserver(adapterprom.HookObserverConfig{
		Registry: registry,
	})
	if err != nil {
		return promStack{}, fmt.Errorf("register cell hook observer: %w", err)
	}
	metricProvider, err := adapterprom.NewMetricProvider(adapterprom.MetricProviderConfig{
		Registry:  registry,
		Namespace: "gocell",
	})
	if err != nil {
		return promStack{}, fmt.Errorf("build metrics provider: %w", err)
	}
	return promStack{
		registry:       registry,
		hookObserver:   hookObserver,
		metricProvider: metricProvider,
	}, nil
}

// buildMetricsHandler constructs the /metrics HTTP handler. When metricsToken
// is set the handler is wrapped with a constant-time token guard; otherwise a
// warning is emitted and the handler is unauthenticated. The production-mode
// "token required" fail-fast is enforced centrally by SharedDeps.Validate so
// this helper only concerns itself with handler construction.
//
// ref: Kubernetes metrics/rbac — control-plane endpoints must be guarded.
func buildMetricsHandler(metricsToken string, registry *prom.Registry) http.Handler {
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Timeout:             metricsScrapeTimeout,
		MaxRequestsInFlight: metricsMaxRequestsInFlight,
		Registry:            registry, // self-instrument: promhttp_metric_handler_errors_total
	})
	if metricsToken != "" {
		return withMetricsTokenGuard(metricsToken, h)
	}
	slog.Warn("GOCELL_METRICS_TOKEN not set; /metrics exposes cell lifecycle signals without authentication (dev mode only)")
	return h
}
