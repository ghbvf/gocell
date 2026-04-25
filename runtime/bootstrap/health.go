package bootstrap

// health.go — HealthRouteGroups factory for the PR-A14b per-listener model.
//
// /healthz, /readyz, and /metrics no longer live on the primary listener's
// outer mux. They are registered as framework-owned RouteGroups on the
// HealthListener (with phase5 fall-back to PrimaryListener when no
// HealthListener is declared — see docs/ops/listener-topology.md). Each
// route lives in its own RouteGroup so callers can attach independent
// per-route auth plans (typically AuthVerboseToken on /readyz).
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
// HealthRouteGroups. Use WithMetricsHandler / WithReadyzAuth / WithLivezAuth
// / WithMetricsAuth.
type HealthRouteGroupOption func(*healthRouteGroupCfg)

type healthRouteGroupCfg struct {
	metricsHandler  http.Handler
	livezAuth       cell.GroupAuth
	readyzAuth      cell.GroupAuth
	metricsAuth     cell.GroupAuth
	verboseDisabled bool   // PR-A35: when true, /readyz?verbose is answered with the plain aggregate body (no internal topology disclosed)
	verboseToken    string // PR-A35 defense-in-depth: handler-level X-Readyz-Token strict gate (in addition to AuthVerboseToken middleware)
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

// WithLivezAuth attaches a GroupAuth plan to the /healthz RouteGroup.
// nil means no group-level auth override; the listener default auth applies.
func WithLivezAuth(a cell.GroupAuth) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.livezAuth = a }
}

// WithReadyzAuth attaches a GroupAuth plan to the /readyz RouteGroup. The
// canonical use is cell.NewAuthVerboseToken("X-Readyz-Token", token) to gate
// ?verbose access with a bearer token.
// nil means no group-level auth override; the listener default auth applies.
func WithReadyzAuth(a cell.GroupAuth) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.readyzAuth = a }
}

// WithMetricsAuth attaches a GroupAuth plan to the /metrics RouteGroup.
// nil means no group-level auth override; the listener default auth applies.
func WithMetricsAuth(a cell.GroupAuth) HealthRouteGroupOption {
	return func(c *healthRouteGroupCfg) { c.metricsAuth = a }
}

// WithReadyzVerboseToken plumbs a verbose-token to the health.Handler's
// strict-gate path (PR-A35 defense-in-depth). Set this alongside
// WithReadyzPolicy(PolicyVerboseToken(..., token)) so that both layers see
// the same secret: the policy middleware 401's at the route group, and the
// handler 401's defensively if a future misconfiguration drops the policy.
//
// Empty token leaves the handler-level gate disabled — verbose requests
// then rely solely on the route-group PolicyVerboseToken (or render plain
// body when no policy is wired and WithReadyzVerboseDisabled is set).
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
// (/healthz, /readyz, optional /metrics) on the HealthListener. Each group
// can carry its own GroupAuth plan via the option helpers above; callers
// wanting a verbose-token gate on /readyz use WithReadyzAuth(cell.NewAuthVerboseToken(...)).
//
// A nil/zero metrics handler omits the /metrics route entirely.
func HealthRouteGroups(h *health.Handler, opts ...HealthRouteGroupOption) []cell.RouteGroup {
	cfg := applyHealthRouteGroupOpts(opts)
	groups := []cell.RouteGroup{
		{
			Listener: cell.HealthListener,
			Auth:     cfg.livezAuth,
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
			Auth:     cfg.readyzAuth,
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
			Auth:     cfg.metricsAuth,
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
