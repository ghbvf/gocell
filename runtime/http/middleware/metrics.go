package middleware

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/observability"
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
// Cell-label resolution goes through the sealed metrics.ResolveCellLabel funnel:
// it reads kernel/ctxkeys.CellID (installed by the router-owned root
// CellAttribution before any short-circuiting protection middleware) and emits
// it only when it is a member of validCellIDs — the assembly's closed cell-id
// set threaded in by the router via WithCellIDClosedSet (M12b #1093). An absent
// or out-of-set cell degrades to metrics.RuntimeCellSentinel, so a non-assembly
// cell id can never pollute the platform SLO series. A nil/empty validCellIDs
// (standalone middleware / tests with no assembly) degrades every cell to the
// sentinel.
//
// Route label resolution uses RouteFor, which reads the dispatch-time
// recorder first and then falls back to the RouteResolver injected by the
// router via WithRouteResolver. This means Metrics, AccessLog, and Tracing
// all see the same route label even on reject paths (auth, rate limit,
// circuit breaker, body limit, 405).
func Metrics(collector metrics.Collector, clk clock.Clock, validCellIDs map[string]struct{}) func(http.Handler) http.Handler {
	return metricsWithClock(collector, clk, validCellIDs)
}

// metricsWithClock is the clock-injectable variant used by Metrics and tests.
func metricsWithClock(collector metrics.Collector, clk clock.Clock, validCellIDs map[string]struct{}) func(http.Handler) http.Handler {
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

			observability.SafeObserve(slog.Default(), func() {
				route := RouteFor(r.Context(), r.Method, r.URL.Path)
				cell := metrics.ResolveCellLabel(r.Context(), validCellIDs)
				collector.RecordRequest(r.Context(), cell, r.Method, route, state.Status(), clk.Since(start).Seconds())
			})
		})
	}
}
