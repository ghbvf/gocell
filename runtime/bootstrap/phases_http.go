package bootstrap

// phases_http.go — HTTP router construction, route group mounting, auth plan
// wiring, and auth validation (phase5).
//
// Covers:
//   - phase5BuildRouters / phase5InitHealthHandler / phase5BuildPerListenerRouters
//   - phase5CollectRouteGroups / phase5MountRouteGroups
//   - phase5FinalizeAllRouters
//   - validateInternalGuardForDeclaredRoutes / declaredInternalRoutes
//   - validateAuthVerifierForDeclaredRoutes
//   - buildListenerRouterOpts / autoWireHTTPMetricsCollector / buildAuthRouterOptions
//
// ref: kubernetes/kubernetes apiserver/pkg/server/genericapiserver.go —
// per-listener apiHandler assembly: each listener gets its own handler chain
// built at startup time, then the listener is sealed before serving begins.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// phase5BuildRouters builds one Router per declared listener, collects
// RouteGroups from all cell snapshots, mounts each group on its listener's
// router, and finalizes auth on the primary router.
//
// PR-A14b: replaces the single-router phase5BuildHTTPRouter. Each listener
// now gets its own chi.Mux root with a policy applied at build time.
//
// ref: go-kratos/kratos app.go — per-server middleware at build time.
func (b *Bootstrap) phase5BuildRouters(s *phaseState) error {
	if err := b.phase5InitHealthHandler(s); err != nil {
		return err
	}
	routers, err := b.phase5BuildPerListenerRouters(s)
	if err != nil {
		return err
	}
	groups := b.phase5CollectRouteGroups(s, routers)
	// defense-in-depth: phase0 already validated; re-check after phase4 resolved verifiers
	if err := b.validateAuthPlanMTLSBindings(); err != nil {
		return err
	}
	if err := b.phase5MountRouteGroups(routers, groups); err != nil {
		return err
	}
	if err := b.phase5FinalizeAllRouters(routers); err != nil {
		return err
	}
	s.routers = routers
	if primaryRtr, ok := routers[cell.PrimaryListener]; ok {
		s.rtr = primaryRtr
	}
	return nil
}

// phase5InitHealthHandler creates the health.Handler and registers checkers.
func (b *Bootstrap) phase5InitHealthHandler(s *phaseState) error {
	cfg := b.resolveHealthRouteGroupCfg()

	var hhOpts []health.Option
	if b.readyzDeadline > 0 {
		hhOpts = append(hhOpts, health.WithDeadline(b.readyzDeadline))
	}
	// PR-A35 + PR-A14b round-3: WithReadyzVerboseDisabled is a
	// HealthRouteGroupOption (no longer a bootstrap-level Option) — peek
	// at the resolved cfg to thread the health.Option to the handler.
	if cfg.verboseDisabled {
		hhOpts = append(hhOpts, health.WithVerboseDisabled())
	}
	hh := health.New(s.asm, b.clock, hhOpts...)
	if b.adapterInfo != nil {
		hh.SetAdapterInfo(b.adapterInfo)
	}
	// PR-A35 defense-in-depth: when WithReadyzVerboseToken is supplied,
	// configure the handler's strict X-Readyz-Token gate too. The
	// PolicyVerboseToken middleware at the route group is the first layer;
	// the handler is the second layer — both consult the same token, so a
	// misconfiguration that drops the middleware still fails closed.
	if cfg.verboseToken != "" {
		hh.SetVerboseToken(cfg.verboseToken)
	}
	s.hh = hh
	s.healthRouteGroupOpts = b.healthRouteGroupOpts
	return b.registerAllHealthCheckers(s)
}

// phase5BuildPerListenerRouters creates one Router per declared listener config.
func (b *Bootstrap) phase5BuildPerListenerRouters(s *phaseState) (map[cell.ListenerRef]*router.Router, error) {
	routers := make(map[cell.ListenerRef]*router.Router, len(b.listenerConfigs))
	for ref, cfg := range b.listenerConfigs {
		rtrOpts, err := b.buildListenerRouterOpts(s, ref, cfg)
		if err != nil {
			return nil, err
		}
		rtr, err := router.NewForListener(ref, rtrOpts...)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: build router for listener %q: %w", ref.String(), err)
		}
		routers[ref] = rtr
	}
	return routers, nil
}

// phase5CollectRouteGroups collects RouteGroups from health and cell snapshots.
// Each RouteGroup is annotated with the contributing cell ID (OPS-02).
//
// Health-listener fallback: when no HealthListener is declared, /healthz and
// /readyz RouteGroups (which target cell.HealthListener) are remapped to the
// PrimaryListener so the probes remain reachable. The /metrics route is excluded
// from this fallback — B2 enforces a dedicated HealthListener when a metrics
// handler is configured (phase0 rejects that combination at startup).
func (b *Bootstrap) phase5CollectRouteGroups(s *phaseState, routers map[cell.ListenerRef]*router.Router) []cell.RouteGroup {
	_, hasHealthListener := routers[cell.HealthListener]
	groups := HealthRouteGroups(s.hh, s.healthRouteGroupOpts...)
	if !hasHealthListener {
		// Remap livez/readyz health groups to PrimaryListener so /healthz and /readyz
		// are served even when no dedicated HealthListener was declared.
		// (B2 has already blocked the metrics-without-HealthListener case at phase0.)
		for i := range groups {
			if groups[i].Listener == cell.HealthListener {
				groups[i].Listener = cell.PrimaryListener
			}
		}
	}
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for i := range snap.RouteGroups {
			rg := snap.RouteGroups[i]
			rg.CellID = id
			groups = append(groups, rg)
		}
	}
	return groups
}

// phase5MountRouteGroups mounts each RouteGroup on its listener's router.
// OPS-02: wraps registration errors with cell ID, group index, listener, and prefix context.
func (b *Bootstrap) phase5MountRouteGroups(routers map[cell.ListenerRef]*router.Router, groups []cell.RouteGroup) error {
	for i, rg := range groups {
		rtr, ok := routers[rg.Listener]
		if !ok {
			return fmt.Errorf("bootstrap: RouteGroup references undeclared listener %q; add WithListener(%s,...) to bootstrap options",
				rg.Listener.String(), rg.Listener.String())
		}
		if rg.Register == nil {
			return fmt.Errorf("bootstrap: RouteGroup for listener %q has nil Register function", rg.Listener.String())
		}
		if err := rtr.MountRouteGroup(rg); err != nil {
			cellID := rg.CellID
			if cellID == "" {
				cellID = "<framework>"
			}
			return fmt.Errorf("cell %s RouteGroup %d (listener=%s, prefix=%q): %w",
				cellID, i, rg.Listener.String(), rg.Prefix, err)
		}
	}
	return nil
}

// phase5FinalizeAllRouters installs /internal/v1/* isolation on the primary
// router and calls FinalizeAuth on every declared listener's router.
//
// CORR-01: previously only the primary router was finalized, silently skipping
// internal-route affinity + policy coverage checks for Internal and Health routers.
//
// PR-258 RES-5: the /internal/v1/* 404 isolation is now installed as an
// early-responder middleware via WithEarlyResponder at router-build time
// (see buildListenerRouterOpts), not as a chi route here. The middleware
// runs BEFORE the policy layer so the contract does not depend on a
// public-matcher exemption.
func (b *Bootstrap) phase5FinalizeAllRouters(routers map[cell.ListenerRef]*router.Router) error {
	// Primary: auth policy check + FinalizeAuth.
	if primaryRtr, ok := routers[cell.PrimaryListener]; ok {
		if err := b.validateAuthVerifierForDeclaredRoutes(cell.PrimaryListener, primaryRtr); err != nil {
			return err
		}
		if err := primaryRtr.FinalizeAuth(); err != nil {
			return fmt.Errorf("bootstrap: primary router finalize auth: %w", err)
		}
	}
	// Internal + Health (and any future listeners): FinalizeAuth only.
	// No auth verifier check (these listeners use service-token / mTLS / none policy).
	refs := make([]cell.ListenerRef, 0, len(routers))
	for ref := range routers {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	for _, ref := range refs {
		if ref == cell.PrimaryListener {
			continue // already handled above
		}
		rtr := routers[ref]
		if err := rtr.FinalizeAuth(); err != nil {
			return fmt.Errorf("bootstrap: %s router finalize auth: %w", ref.String(), err)
		}
		if err := b.validateInternalGuardForDeclaredRoutes(ref, rtr); err != nil {
			return err
		}
	}
	return nil
}

// validateInternalGuardForDeclaredRoutes enforces the internal-listener
// trust boundary once route metadata has been collected. Declaring a
// /internal/v1/* route means the listener must carry a concrete transport
// guard; AuthNone is acceptable only for listeners with no internal routes.
func (b *Bootstrap) validateInternalGuardForDeclaredRoutes(ref cell.ListenerRef, rtr *router.Router) error {
	if ref != cell.InternalListener {
		return nil
	}
	cfg, ok := b.listenerConfigs[ref]
	if !ok || chainContainsInternalGuard(cfg.authChain) {
		return nil
	}
	internalRoutes := declaredInternalRoutes(rtr)
	if len(internalRoutes) == 0 {
		return nil
	}
	sort.Strings(internalRoutes)
	return errcode.New(errcode.ErrCellInvalidConfig,
		fmt.Sprintf("bootstrap: internal listener has %d /internal/v1/* route(s) declared without an internal guard: [%s]; "+
			"set bootstrap.WithListener(cell.InternalListener, ..., []cell.ListenerAuth{cell.MustNewAuthServiceToken(store, ring)}) "+
			"and optionally layer cell.AuthMTLS{} with verified client TLS",
			len(internalRoutes), strings.Join(internalRoutes, ", ")))
}

func declaredInternalRoutes(rtr *router.Router) []string {
	var routes []string
	for _, meta := range rtr.DeclaredAuthMetas() {
		if meta.IsInternal() {
			routes = append(routes, meta.Method+" "+meta.Path)
		}
	}
	return routes
}

// validateAuthVerifierForDeclaredRoutes ensures protected routes mounted on a
// listener are actually gated by an auth-flavored plan chain. The acceptable
// plans are AuthJWT / AuthJWTFromAssembly, AuthMTLS, or AuthServiceToken (or
// any combination). AuthNone chains with protected routes (non-Public,
// non-Internal) cause Run() to fail-closed at phase5.
//
// Uses chainProtectsRoutes (from auth_plan_describe.go) for the typed check,
// replacing the old string-based isAuthFlavoredPolicy check.
func (b *Bootstrap) validateAuthVerifierForDeclaredRoutes(ref cell.ListenerRef, rtr *router.Router) error {
	cfg := b.listenerConfigs[ref]
	if chainProtectsRoutes(cfg.authChain) {
		return nil
	}
	var protected []string
	for _, meta := range rtr.DeclaredAuthMetas() {
		if meta.Public || meta.IsInternal() {
			continue
		}
		protected = append(protected, meta.Method+" "+meta.Path)
	}
	if len(protected) == 0 {
		return nil
	}
	sort.Strings(protected)
	return fmt.Errorf(
		"bootstrap: listener %q has %d protected route(s) declared without an auth plan: [%s]; "+
			"set the listener's auth chain to cell.NewAuthJWT(verifier), cell.NewAuthJWTFromAssembly(asm), "+
			"cell.AuthMTLS{}, or cell.NewAuthServiceToken(...), or mark the route Public or use InternalListener",
		ref.String(), len(protected), strings.Join(protected, ", "),
	)
}

// buildListenerRouterOpts assembles the router.Option slice for a single
// listener's router. JWT auth flows through the listener's authChain:
// applyListenerAuthChain extracts verifier options and non-JWT middleware;
// the JWT verifier is installed via router.WithAuthMiddleware so the router
// can build matcher-aware AuthMiddleware after FinalizeAuth.
func (b *Bootstrap) buildListenerRouterOpts(_ *phaseState, ref cell.ListenerRef, cfg listenerConfig) ([]router.Option, error) {
	opts := make([]router.Option, 0, len(b.routerOpts)+7)
	// Always inject the bootstrap clock into every listener router so that
	// middleware.AccessLog and middleware.Metrics get a real clock. This must
	// come before b.routerOpts so a caller-supplied WithRouterClock can
	// override it.
	opts = append(opts, router.WithRouterClock(b.clock))
	opts = append(opts, b.routerOpts...)

	// R2: auto-wire HTTP metrics collector when a Provider is configured.
	var err error
	opts, err = b.autoWireHTTPMetricsCollector(opts)
	if err != nil {
		return nil, err
	}

	// Primary listener: install the /internal/v1/* 404 isolation as an
	// early-responder middleware so the contract runs BEFORE auth and does
	// NOT require a JWT public-matcher exemption nor a policy-coverage
	// whitelist. PR-258 RES-5 narrowing.
	if ref == cell.PrimaryListener {
		opts = append(opts, router.InternalPrefixIsolationResponder())
	}

	// Apply the listener's AuthPlan chain: extract non-JWT middleware and
	// JWT router options. applyListenerAuthChain handles all typed plan variants.
	if len(cfg.authChain) > 0 {
		mws, routerAuthOpts, _, aerr := b.applyListenerAuthChain(ref, cfg.authChain)
		if aerr != nil {
			return nil, aerr
		}
		if len(mws) > 0 {
			opts = append(opts, router.WithDefaultMiddleware(mws...))
		}
		opts = append(opts, routerAuthOpts...)
	}

	// R2-11: Health and Internal listeners intentionally run without a JWT
	// verifier. Suppress the FinalizeAuth Warn for these listeners.
	if ref == cell.HealthListener || ref == cell.InternalListener {
		opts = append(opts, router.WithSuppressNoAuthVerifierWarn())
	}

	return opts, nil
}

// autoWireHTTPMetricsCollector adds a router.WithMetricsCollector option when
// b.metricsProvider is a real (non-Nop) provider. When no provider is set, the
// opts slice is returned unchanged.
//
// The collector is created ONCE and cached in b.httpCollector so that subsequent
// calls (one per declared listener) reuse the same instrumented collector rather
// than re-registering http_requests_total and http_request_duration_seconds
// against the same Prometheus registry — which would cause a duplicate-registration
// error on the second listener.
//
// R2 wiring rule: if callers already passed router.WithMetricsCollector via
// WithRouterOptions AND also called WithMetricsProvider, the auto-wired
// collector construction will fail with a duplicate-name error. The error is
// wrapped with an actionable message that tells operators which side to remove.
//
// HTTP-METRICS-LABEL-REALIGN: cell labels are resolved by router-owned root
// attribution from RouteGroup ownership. This collector is provider-neutral
// and labels each observation from its RecordRequest cellID argument.
//
// ref: runtime/observability/metrics.NewProviderCollector — provider-neutral
// HTTP collector that records http_requests_total + http_request_duration_seconds.
func (b *Bootstrap) autoWireHTTPMetricsCollector(opts []router.Option) ([]router.Option, error) {
	if b.metricsProvider == nil {
		return opts, nil
	}
	// NopProvider is the default when no provider is injected; skip auto-wire
	// to avoid allocating a no-op collector on every bootstrap startup.
	if _, isNop := b.metricsProvider.(kernelmetrics.NopProvider); isNop {
		return opts, nil
	}
	// Create the collector only once (cached in b.httpCollector) so that
	// multiple calls to buildListenerRouterOpts (one per declared listener)
	// share the same collector and do not attempt to re-register Prometheus
	// counters/histograms with the same names.
	if b.httpCollector == nil {
		collector, err := metricsmiddleware.NewProviderCollector(b.metricsProvider, metricsmiddleware.ProviderCollectorConfig{})
		if err != nil {
			return nil, fmt.Errorf(
				"bootstrap: metrics auto-wire conflict: WithMetricsProvider already constructs the HTTP collector; "+
					"do not also pass router.WithMetricsCollector via WithRouterOptions. "+
					"Remove one side: %w", err)
		}
		b.httpCollector = collector
	}
	return append(opts, router.WithMetricsCollector(b.httpCollector)), nil
}

// buildAuthRouterOptions assembles the auth-middleware and optional metrics
// options for the given verifier.
func (b *Bootstrap) buildAuthRouterOptions(v auth.IntentTokenVerifier) ([]router.Option, error) {
	opts := []router.Option{
		router.WithAuthMiddleware(v),
		router.WithRouterClock(b.clock),
	}
	if b.metricsProvider == nil {
		return opts, nil
	}
	am, err := auth.NewAuthMetrics(b.metricsProvider)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: register auth metrics: %w", err)
	}
	opts = append(opts, router.WithAuthMetrics(am))
	return opts, nil
}
