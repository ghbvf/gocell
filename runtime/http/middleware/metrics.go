package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// RoutePatternResolver maps a concrete request to a low-cardinality route
// pattern when chi did not reach a matched endpoint handler.
type RoutePatternResolver func(method, path string) (string, bool)

// MetricsOption configures the Metrics middleware.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	routeResolver RoutePatternResolver
}

// WithRoutePatternResolver installs a fallback route resolver for pre-handler
// rejects such as auth, rate-limit, circuit-breaker, body-limit, and 405.
func WithRoutePatternResolver(fn RoutePatternResolver) MetricsOption {
	return func(c *metricsConfig) {
		if fn != nil {
			c.routeResolver = fn
		}
	}
}

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector. A clock must be provided; use clock.Real() at
// the composition root.
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), Metrics reuses it. Otherwise it creates its own to
// remain usable as a standalone middleware.
//
// Cell-label resolution reads kernel/ctxkeys.CellID from request context.
// Router-owned root attribution installs that key before short-circuiting
// protection middleware. Absence means framework/runtime traffic and records
// as RuntimeCellIDSentinel.
func Metrics(collector metrics.Collector, clk clock.Clock, opts ...MetricsOption) func(http.Handler) http.Handler {
	return metricsWithClock(collector, clk, opts...)
}

// metricsWithClock is the clock-injectable variant used by Metrics and tests.
func metricsWithClock(collector metrics.Collector, clk clock.Clock, opts ...MetricsOption) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "middleware.Metrics")
	var cfg metricsConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()

			state := RecorderStateFrom(r.Context())
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
			}

			next.ServeHTTP(w, r)

			safeObserve(slog.Default(), func() {
				route := finalMetricsRoute(r, cfg)
				cellID := RuntimeCellIDSentinel
				if v, ok := ctxkeys.CellIDFrom(r.Context()); ok && v != "" {
					cellID = v
				}
				collector.RecordRequest(cellID, r.Method, route, state.Status(), clk.Since(start).Seconds())
			})
		})
	}
}

func finalMetricsRoute(r *http.Request, cfg metricsConfig) string {
	route := RoutePatternFromCtx(r.Context())
	if cfg.routeResolver == nil {
		return route
	}
	if resolved, ok := cfg.routeResolver(r.Method, r.URL.Path); ok && resolved != "" {
		if route == "unmatched" || strings.HasSuffix(route, "/*") {
			return resolved
		}
	}
	return route
}
