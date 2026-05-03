package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// WithCellIDContext returns an HTTP middleware that injects a cell identity
// into the request context using ctxkeys.WithCellID. Bootstrap installs one
// instance per listener (with the framework "_runtime" sentinel) and one per
// RouteGroup (with the cell's actual ID); group-level installation runs after
// listener-level installation, so the per-cell value overrides the runtime
// sentinel for any request that lands inside a registered RouteGroup.
//
// Callers must supply a non-empty cellID; both call sites (router.go literal
// "_runtime" and bootstrap.mountOneRouteGroup guarded by rg.CellID != "")
// already enforce that. The downstream invariant — that ctxkeys.MustCellIDFrom
// always finds a non-empty value — is locked by HTTP-METRICS-LABEL-LISTENER-
// ROOT-RUNTIME-01 archtest and by middleware.Metrics's own panic on missing
// ctx.
//
// ref: kubernetes/kubernetes apiserver/pkg/endpoints/metrics/metrics.go —
// component label captured at registration time as a constant, not derived
// from the request.
func WithCellIDContext(cellID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(ctxkeys.WithCellID(r.Context(), cellID)))
		})
	}
}
