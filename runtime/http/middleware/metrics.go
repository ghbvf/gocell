package middleware

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector. A clock must be provided; use clock.Real() at
// the composition root.
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), Metrics reuses it. Otherwise it creates its own to
// remain usable as a standalone middleware.
//
// Cell-label resolution: this middleware attaches a *cellIDState seeded with
// RuntimeCellIDSentinel to the request context before invoking next. Any
// downstream WithCellIDContext middleware (installed by
// bootstrap.mountOneRouteGroup on a chi sub-mux) mutates that state to its
// cell's ID. After next returns the recorder reads the resolved value from
// the same struct — chi's RouteContext uses the same mutable-pointer pattern
// so outer middleware can observe values written by sub-mux dispatch.
// Framework-owned paths (healthz, readyz, /metrics, unmatched 404s,
// listeners with no business RouteGroup) keep the sentinel.
func Metrics(collector metrics.Collector, clk clock.Clock) func(http.Handler) http.Handler {
	return metricsWithClock(collector, clk)
}

// metricsWithClock is the clock-injectable variant used by Metrics and tests.
func metricsWithClock(collector metrics.Collector, clk clock.Clock) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "middleware.Metrics")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()

			state := RecorderStateFrom(r.Context())
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
			}

			ctx, cs := withCellIDState(r.Context(), RuntimeCellIDSentinel)
			next.ServeHTTP(w, r.WithContext(ctx))

			safeObserve(slog.Default(), func() {
				route := RoutePatternFromCtx(r.Context())
				collector.RecordRequest(cs.cellID, r.Method, route, state.Status(), clk.Since(start).Seconds())
			})
		})
	}
}
