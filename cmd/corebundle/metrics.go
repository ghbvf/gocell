// metrics.go: Prometheus 指标栈构建与 /metrics handler 守卫。
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	adapterprom "github.com/ghbvf/gocell/adapters/prometheus"
)

// metricsAuthHeader names the request header used to authenticate
// /metrics scrapers when a bearer token is configured. Mirrors the X-Readyz-Token
// convention for /readyz?verbose — keeping the same shape for all
// control-plane endpoints lets operators standardize scraper config.
const metricsAuthHeader = "X-Metrics-Token"

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
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	if metricsToken != "" {
		return withMetricsTokenGuard(metricsToken, h)
	}
	slog.Warn("GOCELL_METRICS_TOKEN not set; /metrics exposes cell lifecycle signals without authentication (dev mode only)")
	return h
}
