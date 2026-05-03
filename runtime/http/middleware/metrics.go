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
// The cell identity stamped on every emitted observation is read from the
// request context via ctxkeys.MustCellIDFrom — bootstrap installs
// WithCellIDContext middleware at the listener-root layer (with the framework
// "_runtime" sentinel) and at the route-group layer (with each cell's ID), so
// the value is always present on requests that reach this middleware. A
// missing value indicates a framework wiring bug; the panic surfaces it at
// startup or in tests rather than silently emitting metrics under a fallback.
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
				route := RoutePatternFromCtx(r.Context())
				cellID := ctxkeys.MustCellIDFrom(r.Context())
				collector.RecordRequest(cellID, r.Method, route, state.Status(), clk.Since(start).Seconds())
			})
		})
	}
}
