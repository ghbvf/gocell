package middleware

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
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
// Cell-label resolution reads kernel/ctxkeys.CellID from request context.
// Router-owned root attribution installs that key before short-circuiting
// protection middleware. Absence means framework/runtime traffic and records
// as RuntimeCellIDSentinel.
//
// Route label resolution uses RouteFor, which reads the dispatch-time
// recorder first and then falls back to the RouteResolver injected by the
// router via WithRouteResolver. This means Metrics, AccessLog, and Tracing
// all see the same route label even on reject paths (auth, rate limit,
// circuit breaker, body limit, 405).
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

			next.ServeHTTP(w, r)

			safeObserve(slog.Default(), func() {
				route := RouteFor(r.Context(), r.Method, r.URL.Path)
				cellID := RuntimeCellIDSentinel
				if v, ok := ctxkeys.CellIDFrom(r.Context()); ok && v != "" {
					cellID = v
				}
				collector.RecordRequest(cellID, r.Method, route, state.Status(), clk.Since(start).Seconds())
			})
		})
	}
}
