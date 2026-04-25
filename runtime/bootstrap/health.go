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
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/health"
)

// Framework-internal ContractSpecs for health probe endpoints. These are not
// business contracts and are exempt from FMT-18 cross-validation because they
// live in runtime/ (not cells/ or contracts/) and are registered by bootstrap
// itself rather than by a Cell RouteGroups implementation.
var (
	specHealthLivez = wrapper.ContractSpec{
		ID: "http.framework.health.livez.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/healthz",
	}
	specHealthReadyz = wrapper.ContractSpec{
		ID: "http.framework.health.readyz.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/readyz",
	}
	specHealthMetrics = wrapper.ContractSpec{
		ID: "http.framework.health.metrics.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/metrics",
	}
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
			// Use auth.Mount with Public:true so that when health endpoints
			// fall back to the PrimaryListener (no separate HealthListener
			// declared), the auth middleware treats them as public probes.
			// On the HealthListener (no auth middleware), this is a no-op.
			Register: func(mux cell.RouteMux) {
				auth.Mount(mux, auth.Route{
					Contract: specHealthLivez,
					Handler:  h.LivezHandler(),
					Public:   true,
				})
				auth.Mount(mux, auth.Route{
					Contract: specHealthReadyz,
					Handler:  h.ReadyzHandler(),
					Public:   true,
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
				auth.Mount(mux, auth.Route{
					Contract: specHealthMetrics,
					Handler:  mh,
					Public:   true,
				})
			},
		})
	}
	return groups
}
