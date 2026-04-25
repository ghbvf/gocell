package bootstrap

// health.go — HealthRouteGroups factory for the PR-A14b per-listener model.
//
// /healthz, /readyz, and /metrics no longer live on the primary listener's
// outer mux. They are registered as framework-owned RouteGroups on the
// HealthListener (with phase5 fall-back to PrimaryListener when no
// HealthListener is declared — see docs/ops/listener-topology.md). Each
// route lives in its own RouteGroup so callers can attach independent
// per-route policies (typically PolicyVerboseToken on /readyz).
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
// HealthRouteGroups. Use WithMetricsHandler / WithReadyzPolicy / WithLivezPolicy
// / WithMetricsPolicy.
type HealthRouteGroupOption func(*healthRouteGroupCfg)

type healthRouteGroupCfg struct {
	metricsHandler  http.Handler
	livezPolicy     cell.Policy
	readyzPolicy    cell.Policy
	metricsPolicy   cell.Policy
	verboseDisabled bool // PR-A35: when true, /readyz?verbose is answered with the plain aggregate body (no internal topology disclosed)
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

// WithLivezPolicy attaches a cell.Policy to the /healthz RouteGroup.
// Zero value (cell.Policy{}) means inherit the listener default policy.
func WithLivezPolicy(p cell.Policy) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.livezPolicy = p }
}

// WithReadyzPolicy attaches a cell.Policy to the /readyz RouteGroup. The
// canonical use is bootstrap.PolicyVerboseToken to gate ?verbose access with
// a bearer token; any cell.Policy is accepted.
// Zero value (cell.Policy{}) means inherit the listener default policy.
func WithReadyzPolicy(p cell.Policy) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.readyzPolicy = p }
}

// WithMetricsPolicy attaches a cell.Policy to the /metrics RouteGroup.
// Zero value (cell.Policy{}) means inherit the listener default policy.
func WithMetricsPolicy(p cell.Policy) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.metricsPolicy = p }
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
// (/healthz, /readyz, optional /metrics) on the HealthListener. Each group
// can carry its own Policy via the option helpers above; callers wanting a
// verbose-token gate on /readyz attach PolicyVerboseToken via WithReadyzPolicy.
//
// A nil/zero metrics handler omits the /metrics route entirely.
func HealthRouteGroups(h *health.Handler, opts ...HealthRouteGroupOption) []cell.RouteGroup {
	cfg := applyHealthRouteGroupOpts(opts)
	groups := []cell.RouteGroup{
		{
			Listener: cell.HealthListener,
			Policy:   cfg.livezPolicy,
			// auth.Mount with Public:true means the auth middleware (when the
			// group falls back to PrimaryListener and JWT runs there) treats
			// /healthz as a public probe. On the HealthListener (no JWT) it
			// is a no-op annotation.
			Register: func(mux cell.RouteMux) {
				auth.Mount(mux, auth.Route{
					Contract: specHealthLivez,
					Handler:  h.LivezHandler(),
					Public:   true,
				})
			},
		},
		{
			Listener: cell.HealthListener,
			Policy:   cfg.readyzPolicy,
			Register: func(mux cell.RouteMux) {
				auth.Mount(mux, auth.Route{
					Contract: specHealthReadyz,
					Handler:  h.ReadyzHandler(),
					Public:   true,
				})
			},
		},
	}
	if cfg.metricsHandler != nil {
		mh := cfg.metricsHandler
		groups = append(groups, cell.RouteGroup{
			Listener: cell.HealthListener,
			Policy:   cfg.metricsPolicy,
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
