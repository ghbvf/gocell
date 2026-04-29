package bootstrap

// health.go — HealthRouteGroups factory for the PR-A14b per-listener model.
//
// /healthz, /readyz, and /metrics live as framework-owned RouteGroups on the
// HealthListener (with phase5 fall-back to PrimaryListener when no
// HealthListener is declared — see docs/ops/listener-topology.md). Each
// route inherits its listener's auth chain — there is no per-route auth
// plan (PR269 round-3: auth scheme is a listener-scope concern; verbose-mode
// disclosure on /readyz is a separate concern handled by the health handler
// via WithReadyzVerboseToken).
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

// HealthRouteGroupOption customises the route groups returned by
// HealthRouteGroups. Use WithMetricsHandler / WithReadyzVerboseToken /
// WithReadyzVerboseDisabled.
type HealthRouteGroupOption func(*healthRouteGroupCfg)

type healthRouteGroupCfg struct {
	metricsHandler  http.Handler
	verboseDisabled bool   // PR-A35: when true, /readyz?verbose is answered with the plain aggregate body (no internal topology disclosed)
	verboseToken    string // handler-level X-Readyz-Token strict gate (PR-A35 + PR269 round-3: now the single source of verbose-token configuration)
}

// applyHealthRouteGroupOpts evaluates a slice of HealthRouteGroupOption against
// a fresh healthRouteGroupCfg and returns the resulting config. Used both by
// HealthRouteGroups itself and by Bootstrap.resolveHealthRouteGroupCfg to peek
// at "did the caller set a metrics handler?" during phase0 validation.
func applyHealthRouteGroupOpts(opts []HealthRouteGroupOption) healthRouteGroupCfg {
	var cfg healthRouteGroupCfg
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// resolveHealthRouteGroupCfg evaluates the accumulated WithHealthRoutes
// options once so phase0 / phase5 can inspect the resolved configuration
// without applying side-effects.
func (b *Bootstrap) resolveHealthRouteGroupCfg() healthRouteGroupCfg {
	return applyHealthRouteGroupOpts(b.healthRouteGroupOpts)
}

// WithMetricsHandler installs an http.Handler at /metrics on the HealthListener.
// nil is a no-op (the /metrics route is not registered).
func WithMetricsHandler(h http.Handler) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.metricsHandler = h }
}

// WithReadyzVerboseToken plumbs a verbose-mode disclosure token to the
// health.Handler's strict-gate path. Requests with ?verbose=true must carry a
// matching X-Readyz-Token header; mismatches receive 401 ErrReadyzVerboseDenied
// from health.Handler.SetVerboseToken / sendVerboseDenied (canonical envelope
// via httputil.WritePublicError).
//
// Note: verbose-token is a disclosure gate, not an authentication scheme — it
// only controls whether the verbose body is rendered. Listener-level auth
// (cell.NewAuthJWTFromAssembly, cell.NewAuthServiceToken, etc.) is orthogonal.
//
// Empty token leaves the gate disabled — verbose requests then render plain
// body unless WithReadyzVerboseDisabled is set.
func WithReadyzVerboseToken(token string) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.verboseToken = token }
}

// WithReadyzVerboseDisabled suppresses the /readyz?verbose body entirely.
// The endpoint still answers, but the aggregate body is returned regardless
// of the ?verbose query parameter — internal topology (cell names, dependency
// names) is never disclosed.
//
// Use this for ephemeral deployments (test harnesses, single-node demos)
// that waive the verbose debug channel. Production deployments should attach
// PolicyVerboseToken instead so operators retain a token-gated diagnostic
// path. Equivalent to PR-A35's bootstrap.WithVerboseDisabled, threaded
// through the WithHealthRoutes option pattern.
func WithReadyzVerboseDisabled() HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.verboseDisabled = true }
}

// HealthRouteGroups returns one RouteGroup per framework-owned health route
// (/healthz, /readyz, optional /metrics) on the HealthListener. The HealthListener
// is wired with no listener auth (cmd/corebundle pattern), so health probes are
// reachable without a token; the verbose disclosure on /readyz is gated by
// WithReadyzVerboseToken at the handler layer.
//
// A nil/zero metrics handler omits the /metrics route entirely.
func HealthRouteGroups(h *health.Handler, opts ...HealthRouteGroupOption) []cell.RouteGroup {
	cfg := applyHealthRouteGroupOpts(opts)
	groups := []cell.RouteGroup{
		{
			Listener: cell.HealthListener,
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{
					Contract: specHealthLivez,
					Handler:  h.LivezHandler(),
					Public:   true,
				})
				return nil
			},
		},
		{
			Listener: cell.HealthListener,
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{
					Contract: specHealthReadyz,
					Handler:  h.ReadyzHandler(),
					Public:   true,
				})
				return nil
			},
		},
	}
	if cfg.metricsHandler != nil {
		mh := cfg.metricsHandler
		groups = append(groups, cell.RouteGroup{
			Listener: cell.HealthListener,
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{
					Contract: specHealthMetrics,
					Handler:  mh,
					Public:   true,
				})
				return nil
			},
		})
	}
	return groups
}
