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
//   - buildListenerRouterOpts / autoWireHTTPMetricsCollector
//   - buildAuthRouterOptions
//
// ref: kubernetes/kubernetes apiserver/pkg/server/genericapiserver.go —
// per-listener apiHandler assembly: each listener gets its own handler chain
// built at startup time, then the listener is sealed before serving begins.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/devtools"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	runtimewebhook "github.com/ghbvf/gocell/runtime/webhook"
)

// phase5BuildRouters builds one Router per declared listener, collects
// RouteGroups from all cell snapshots, mounts each group on its listener's
// router, and finalizes auth on the primary router.
//
// PR-A14b: replaces the single-router phase5BuildHTTPRouter. Each listener
// now gets its own stdlib *http.ServeMux root with a policy applied at build time.
//
// ref: go-kratos/kratos app.go — per-server middleware at build time.
func (b *Bootstrap) phase5BuildRouters(ctx context.Context, s *phaseState) error {
	if err := b.phase5InitHealthHandler(s); err != nil {
		return err
	}
	if err := b.phase5InitDevtoolsHandler(ctx, s); err != nil {
		return err
	}
	routers, err := b.phase5BuildPerListenerRouters(s)
	if err != nil {
		return err
	}
	groups := b.phase5CollectRouteGroups(s)
	webhookGroups, err := b.phase5DrainWebhookReceivers(s)
	if err != nil {
		return err
	}
	groups = append(groups, webhookGroups...)
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
	// Note: the /readyz per-probe deadline (WithReadyzDeadline) is owned by the
	// healthz.Aggregator, not the Handler — it is applied to the default
	// aggregator at construction in phase0ValidateOptions.
	// PR-A35 + PR-A14b round-3: WithReadyzVerboseDisabled is a
	// HealthRouteGroupOption (no longer a bootstrap-level Option) — peek
	// at the resolved cfg to thread the health.Option to the handler.
	if cfg.verboseDisabled {
		hhOpts = append(hhOpts, health.WithVerboseDisabled())
	}
	hh := health.New(s.asm, b.healthAggregator, b.clock, hhOpts...)
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
	return b.drainProbes(s)
}

// phase5BuildPerListenerRouters creates one Router per declared listener config.
func (b *Bootstrap) phase5BuildPerListenerRouters(s *phaseState) (map[cell.ListenerRef]*router.Router, error) {
	routers := make(map[cell.ListenerRef]*router.Router, len(b.listenerConfigs))
	for ref, cfg := range b.listenerConfigs {
		rtrOpts, err := b.buildListenerRouterOpts(s, ref, cfg)
		if err != nil {
			return nil, err
		}
		rtr, err := router.NewForListener(b.clock, ref, rtrOpts...)
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
// Framework health groups (/healthz, /readyz, /metrics) always target
// cell.HealthListener. There is no fallback remap onto PrimaryListener — #673
// removed the silent relocation that collapsed port-level isolation. phase0
// (validateHTTPListenerConfigs) guarantees a HealthListener is declared, so
// the routers map below always contains it.
func (b *Bootstrap) phase5CollectRouteGroups(s *phaseState) []cell.RouteGroup {
	groups := HealthRouteGroups(s.hh, s.healthRouteGroupOpts...)
	if s.devtoolsHandler != nil {
		groups = append(groups, devtools.RouteGroup(s.devtoolsHandler))
	}
	// Framework projection rebuild control-plane endpoint (opt-in via
	// WithProjectionRebuildEndpoint). phase0 (validateProjectionRebuildEndpoint)
	// guaranteed the AdminListener exists when enabled.
	if b.projectionRebuildEnabled {
		groups = append(groups, b.projectionRebuildRouteGroup())
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
		// #673: cell.HealthListener is reserved for framework-owned health routes
		// (/healthz, /readyz, /metrics; CellID==""). A cell-owned RouteGroup
		// (CellID!="") targeting it would expose business endpoints on the
		// unauthenticated loopback probe port — reject at mount time, mirroring
		// the InternalListener boundary (validateInternalGuardForDeclaredRoutes).
		if rg.Listener == cell.HealthListener && rg.CellID != "" {
			return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"bootstrap: cell-owned RouteGroup must not target cell.HealthListener "+
					"(reserved for framework /healthz, /readyz, /metrics); declare it on "+
					"cell.PrimaryListener or cell.InternalListener instead",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("cellID=%q prefix=%q", rg.CellID, rg.Prefix))))
		}
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
			return fmt.Errorf("cell %s RouteGroup %d/%d (listener=%s, prefix=%q): %w",
				cellID, i, len(groups), rg.Listener.String(), rg.Prefix, err)
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
	return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
		"bootstrap: /internal/v1/* route(s) declared without an internal guard; "+
			"set bootstrap.WithListener(cell.InternalListener, ..., "+
			"[]kauth.ListenerAuth{<svcTokenAuth from kauth.NewAuthServiceToken(store, ring)>}) "+
			"and optionally layer kauth.AuthMTLS{} with verified client TLS",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
			"count=%d routes=[%s]", len(internalRoutes), strings.Join(internalRoutes, ", "),
		))))
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
			"set the listener's auth chain to kauth.NewAuthJWT(verifier), kauth.NewAuthJWTFromAssembly(asm), "+
			"kauth.AuthMTLS{}, or kauth.NewAuthServiceToken(...), or mark the route Public or use InternalListener",
		ref.String(), len(protected), strings.Join(protected, ", "),
	)
}

// buildListenerRouterOpts assembles the router.Option slice for a single
// listener's router. JWT auth flows through the listener's authChain:
// applyListenerAuthChain extracts verifier options and non-JWT middleware;
// the JWT verifier is installed via router.WithAuthMiddleware so the router
// can build matcher-aware AuthMiddleware after FinalizeAuth.
func (b *Bootstrap) buildListenerRouterOpts(s *phaseState, ref cell.ListenerRef, cfg listenerConfig) ([]router.Option, error) {
	opts := make([]router.Option, 0, len(b.routerOpts)+8)
	opts = append(opts, b.routerOpts...)

	// M12b (#1093): thread the assembly's closed cell-id set into every
	// listener's router so the Metrics / BodyLimit middleware validates the
	// request cell label against it at the metric write point (out-of-set →
	// _runtime sentinel, via the sealed metrics.ResolveCellLabel funnel). The set
	// is the same s.asm.CellIDs() that phase5CollectRouteGroups uses to annotate
	// RouteGroup ownership, so the runtime defense and the build-time closed set
	// share one source.
	opts = append(opts, router.WithCellIDClosedSet(s.asm.CellIDs()))

	// R2: auto-wire HTTP metrics collector when a Provider is configured.
	var err error
	opts, err = b.autoWireHTTPMetricsCollector(opts)
	if err != nil {
		return nil, err
	}

	// Auto-wire the idempotency metrics collector — not gated on
	// WithIdempotencyStore, mirroring the HTTP collector. The metric family is
	// REGISTERED whenever a real Provider is configured (so it has a HELP line in
	// /metrics), but its counter SERIES are only emitted when an idempotency
	// store is also wired: buildMux constructs the idempotency middleware only
	// then, and the observer is unused otherwise. So a Provider-configured
	// assembly without idempotency shows idempotency_requests_total in HELP with
	// no time series until the first idempotent request flows.
	opts, err = b.autoWireIdempotencyMetricsCollector(opts)
	if err != nil {
		return nil, err
	}

	// Primary listener: install the /internal/v1/* and /admin/v1/* 404 isolation
	// as early-responder middleware so the contract runs BEFORE auth and does NOT
	// require a JWT public-matcher exemption nor a policy-coverage whitelist.
	// PR-258 RES-5 narrowing; admin counterpart added in #1505 so an undeclared
	// /admin/v1/* probe to the public port 404s (symmetric with /internal/v1/*)
	// instead of 401-ing through the JWT chain.
	if ref == cell.PrimaryListener {
		opts = append(opts,
			router.InternalPrefixIsolationResponder(),
			router.AdminPrefixIsolationResponder())
		// #1348 Batch-C: install the Authorizer injector middleware when an
		// Authorizer has been wired via WithPrimaryAuthorizer. The injector runs
		// as default middleware (AFTER early-responders, BEFORE auth/route
		// handlers) so RequirePermission policies always find the PDP.
		// Only the primary listener receives the injector; Internal, Health, and
		// Admin listeners do not carry ABAC-gated business routes.
		if b.primaryAuthorizer != nil {
			opts = append(opts, router.WithDefaultMiddleware(authorizerInjector(b.primaryAuthorizer)))
		}
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

	// R2-11: Health, Internal, and Admin listeners intentionally run without a
	// JWT verifier — Health is loopback-isolated, Internal carries
	// ServiceToken/mTLS, and Admin (#1505) carries the operator-credential gate
	// (AuthOperator). phase0 affinity guarantees AdminListener never carries a JWT
	// plan, so the FinalizeAuth "no JWT verifier" Warn is noise on all three.
	if ref == cell.HealthListener || ref == cell.InternalListener || ref == cell.AdminListener {
		opts = append(opts, router.WithSuppressNoAuthVerifierWarn())
	}

	return opts, nil
}

// authorizerInjector returns a middleware that injects the given auth.Authorizer
// into every request's context via auth.WithAuthorizer. It is installed on the
// primary listener only (see buildListenerRouterOpts) so that route Policies
// built with auth.RequirePermission can locate the PDP without any cell code
// calling WithAuthorizer directly.
//
// This is the symmetric counterpart to the JWT AuthMiddleware: both inject
// per-request state before route handlers run, and neither is a route policy.
//
// Cognitive complexity: 1 (single path, no branches).
func authorizerInjector(a auth.Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(auth.WithAuthorizer(r.Context(), a))
			next.ServeHTTP(w, r)
		})
	}
}

// autoWireHTTPMetricsCollector adds a router.WithMetricsCollector option when
// b.metricsProvider is a real (non-Nop) provider. When no provider is set, the
// opts slice is returned unchanged.
//
// The collector is created ONCE and cached in b.httpCollector (via the shared
// autoWireCachedCollector funnel) so that subsequent calls (one per declared
// listener) reuse the same instrumented collector rather than re-registering
// http_requests_total and http_request_duration_seconds against the same
// Prometheus registry — which would cause a duplicate-registration error on the
// second listener.
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
	collector, wired, err := autoWireCachedCollector(b, &b.httpCollector,
		func(p kernelmetrics.Provider) (metricsmiddleware.Collector, error) {
			return metricsmiddleware.NewProviderCollector(p, metricsmiddleware.ProviderCollectorConfig{})
		},
		"bootstrap: metrics auto-wire conflict: WithMetricsProvider already constructs the HTTP collector; "+
			"do not also pass router.WithMetricsCollector via WithRouterOptions. Remove one side")
	if err != nil {
		return nil, err
	}
	if !wired {
		return opts, nil
	}
	return append(opts, router.WithMetricsCollector(collector)), nil
}

// autoWireIdempotencyMetricsCollector adds a router.WithIdempotencyMetrics
// option when b.metricsProvider is a real (non-Nop) provider. When no provider
// is set, the opts slice is returned unchanged.
//
// Like autoWireHTTPMetricsCollector, the collector is created ONCE and cached in
// b.idempotencyCollector via the shared autoWireCachedCollector funnel so every
// listener router reuses the same instrument rather than re-registering the
// fixed-name idempotency_requests_total family (the second listener would
// otherwise hit a duplicate-registration error). There is no warn-then-degrade
// path: a registration conflict is startup-fatal.
//
// ref: runtime/observability/metrics.NewIdempotencyCollector — provider-neutral
// collector that records idempotency_requests_total{cell,state}; the cell label
// is derived per-record from request context.
func (b *Bootstrap) autoWireIdempotencyMetricsCollector(opts []router.Option) ([]router.Option, error) {
	collector, wired, err := autoWireCachedCollector(b, &b.idempotencyCollector,
		metricsmiddleware.NewIdempotencyCollector,
		"bootstrap: metrics auto-wire conflict: WithMetricsProvider already constructs the idempotency collector; "+
			"do not also pass router.WithIdempotencyMetrics via WithRouterOptions. Remove one side")
	if err != nil {
		return nil, err
	}
	if !wired {
		return opts, nil
	}
	return append(opts, router.WithIdempotencyMetrics(collector)), nil
}

// buildAuthRouterOptions assembles the auth-middleware and optional metrics
// options for the given verifier.
func (b *Bootstrap) buildAuthRouterOptions(v kauth.IntentTokenVerifier) ([]router.Option, error) {
	opts := []router.Option{
		router.WithAuthMiddleware(v),
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

// phase5DrainWebhookReceivers collects all WebhookReceiverRequest entries from
// cell snapshots and converts them into RouteGroups via
// [runtimewebhook.BuildRouteGroups].
//
// CellID drift check: each request must carry the same cell ID as the snapshot
// key (mirroring drainCellSubscriptions). Mismatches are a codegen defect.
//
// Empty collection is a no-op (returns nil, nil). Non-empty collection
// requires both webhookSourceStore and webhookClaimer to be configured; if
// either is nil, the function returns an errcode explaining which option is missing.
func (b *Bootstrap) phase5DrainWebhookReceivers(s *phaseState) ([]cell.RouteGroup, error) {
	var reqs []cell.WebhookReceiverRequest
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, req := range snap.WebhookReceivers {
			if req.Spec.CellID != id {
				return nil, fmt.Errorf(
					"bootstrap: cell %s webhook receiver drift: declared CellID=%q but snapshot owner=%q"+
						" (codegen should inject cellID from cell metadata; check cellgen + contractgen templates)",
					id, req.Spec.CellID, id,
				)
			}
			reqs = append(reqs, req)
		}
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	if b.webhookSourceStore == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: webhook receivers declared but no source store configured; "+
				"add WithWebhookSourceStore to bootstrap options")
	}
	if b.webhookClaimer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: webhook receivers declared but no claimer configured; "+
				"add WithWebhookClaimer to bootstrap options")
	}
	rec, err := b.autoWireWebhookMetricsCollector()
	if err != nil {
		return nil, err
	}
	groups, err := runtimewebhook.BuildRouteGroups(b.clock, reqs, b.webhookSourceStore, b.webhookClaimer, rec)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: build webhook route groups: %w", err)
	}
	return groups, nil
}
