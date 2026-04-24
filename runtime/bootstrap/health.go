package bootstrap

// health.go — HealthRouteGroups factory for the PR-A14b per-listener model.
//
// In the new architecture, /healthz, /readyz, and /metrics no longer live on
// the primary listener's outer mux. Instead they are registered as a
// RouteGroupContributor on the HealthListener. Bootstrap calls HealthRouteGroups
// during phase5 to collect these groups alongside cell-contributed groups.
//
// ref: go-kratos/kratos transport/http/server.go — infra endpoints on separate server.
// ref: kubernetes/apiserver pkg/server/healthz — health endpoints isolated from API.

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/health"
)

// HealthRouteGroups returns the RouteGroups that mount health and metrics
// endpoints on the HealthListener router. Bootstrap calls this during phase5
// to register the infra endpoints before cell RouteGroups are mounted.
//
// Endpoints registered:
//   - GET /healthz  → h.LivezHandler()
//   - GET /readyz   → h.ReadyzHandler()
//   - GET /metrics  → metricsHandler (when non-nil)
//
// A nil metricsHandler is valid; the /metrics route is simply not registered.
func HealthRouteGroups(h *health.Handler, metricsHandlers ...http.Handler) []cell.RouteGroup {
	groups := []cell.RouteGroup{
		{
			Listener: cell.HealthListener,
			// Use auth.Declare with Public:true so that when health endpoints
			// fall back to the PrimaryListener (no separate HealthListener
			// declared), the auth middleware treats them as public probes.
			// On the HealthListener (no auth middleware), this is a no-op.
			Register: func(mux cell.RouteMux) {
				auth.Declare(mux, auth.RouteDecl{
					Method:  "GET",
					Path:    "/healthz",
					Handler: h.LivezHandler(),
					Public:  true,
				})
				auth.Declare(mux, auth.RouteDecl{
					Method:  "GET",
					Path:    "/readyz",
					Handler: h.ReadyzHandler(),
					Public:  true,
				})
			},
		},
	}
	for _, mh := range metricsHandlers {
		if mh == nil {
			continue
		}
		mh := mh // capture
		groups = append(groups, cell.RouteGroup{
			Listener: cell.HealthListener,
			Register: func(mux cell.RouteMux) {
				auth.Declare(mux, auth.RouteDecl{
					Method:  "GET",
					Path:    "/metrics",
					Handler: mh,
					Public:  true,
				})
			},
		})
	}
	return groups
}
