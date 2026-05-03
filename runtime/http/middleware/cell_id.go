package middleware

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// RuntimeCellIDSentinel is the framework "owner" used as the cell label for
// requests that do not match any cell-owned RouteGroup (healthz, readyz, the
// metrics endpoint itself, unmatched 404s, listeners with no business
// RouteGroup attached). The string is part of the operator-visible Prometheus
// label space, so dashboards / alert rules can match `cell="_runtime"` for
// framework traffic.
const RuntimeCellIDSentinel = "_runtime"

// cellIDState holds the cell identity for the current in-flight request. It
// is intentionally a pointer to a mutable struct rather than a string-valued
// context value so that metrics middleware running on the *root* mux can
// observe a value written by sub-mux middleware after chi dispatch — see
// chi's RouteContext for the same pattern.
//
// Why mutable state rather than ctxkeys.WithCellID alone: chi sub-mux
// middleware that calls r.WithContext(WithValue(ctx, CellID, x)) only
// affects the *child* call chain. Middleware further out (root-mux
// metrics) keeps its own ctx and would read the stale outer value. By
// storing a *cellIDState in ctx at the root and having sub-mux middleware
// mutate the embedded field instead of replacing the ctx, the metrics
// recorder sees the cell-resolved value when it fires after next.ServeHTTP
// returns.
type cellIDState struct {
	cellID string
}

type cellIDStateKeyT struct{}

var cellIDStateKey = cellIDStateKeyT{}

// withCellIDState attaches a fresh cellIDState (initialized to the supplied
// sentinel) to ctx. Used by middleware.Metrics at the listener-root layer.
func withCellIDState(ctx context.Context, sentinel string) (context.Context, *cellIDState) {
	cs := &cellIDState{cellID: sentinel}
	return context.WithValue(ctx, cellIDStateKey, cs), cs
}

// cellIDStateFrom retrieves the in-flight cellIDState attached by Metrics
// at the listener-root layer. Returns nil when no metrics layer is upstream
// (e.g. middleware tested in isolation without a router) — callers must
// handle the nil case rather than dereferencing.
func cellIDStateFrom(ctx context.Context) *cellIDState {
	if cs, ok := ctx.Value(cellIDStateKey).(*cellIDState); ok {
		return cs
	}
	return nil
}

// WithCellIDContext returns an HTTP middleware that records a cell identity
// for the current request in two channels:
//
//   - It writes ctxkeys.CellID via context.WithValue, so handlers and
//     downstream middleware (logging, tracing) read the cell ID through the
//     usual ctxkeys.CellIDFrom helper.
//   - It mutates the *cellIDState attached upstream by middleware.Metrics
//     so the root-mux recorder, which fires *after* this sub-mux middleware
//     returns, observes the per-cell value rather than the sentinel
//     installed at the listener-root.
//
// bootstrap.mountOneRouteGroup installs this middleware on every cell-owned
// RouteGroup with the cell's CellID; framework-owned routes (healthz / readyz
// / the metrics endpoint itself / unmatched paths) leave the metrics
// recorder's default sentinel in place.
//
// Empty-cellID guard: an empty cellID is silently ignored on the metrics
// state — the upstream sentinel survives so dashboards keep matching the
// framework label. The ctxkeys side is also skipped so logging/tracing do
// not record a misleading empty cell ID. PANIC-REGISTERED-01 (ADR) prohibits
// fail-fast panic at this layer, and bootstrap.mountOneRouteGroup already
// guards rg.CellID != "" before installing this middleware, so the only way
// to reach the empty branch is a future caller that constructs the handler
// directly — that caller's mistake should not corrupt the cell label space.
//
// ref: go-chi/chi RouteContext — same mutable-pointer pattern for letting
// outer middleware observe a value resolved by sub-mux dispatch.
// ref: kubernetes apiserver pkg/endpoints/metrics/metrics.go — component
// label captured at registration time as a constant.
func WithCellIDContext(cellID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if cellID == "" {
				next.ServeHTTP(w, r)
				return
			}
			if cs := cellIDStateFrom(ctx); cs != nil {
				cs.cellID = cellID
			}
			next.ServeHTTP(w, r.WithContext(ctxkeys.WithCellID(ctx, cellID)))
		})
	}
}
