package bootstrap

// bootstrap_phases.go — phase method implementations for Bootstrap.Run().
//
// Each phase has cognitive complexity ≤ 15; Run() itself only calls phases.
//
// ref: uber-go/fx app.go (Run splits startup/shutdown via StartTimeout/StopTimeout)
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go (engageStopProcedure LIFO teardown)

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/worker"
)

// PR-258 RES-5: frameworkPrimaryWhitelist is gone. The internal-prefix
// isolation 404 used to require a policy-coverage whitelist entry because
// the handler was a chi route registered without auth.Mount. After
// RES-5 the isolation runs as router early-responder middleware before
// FinalizeAuth's policy-coverage walk reaches it, so no whitelist hole is
// needed.
// Health probes (/healthz, /readyz) are declared via auth.MustMount(Public:true)
// inside HealthRouteGroups and do not need any whitelist entry.

// phaseState extends runState with phase-local values that must be shared
// across phases but are not part of the teardown/error-channel core.
// All fields are set during their respective phase and read by later phases.
type phaseState struct {
	*runState

	// set by phase1
	cfg        config.Config
	cfgWatcher *config.Watcher

	// set by phase2
	pub outbox.Publisher
	sub outbox.Subscriber

	// set by phase3
	asm     *assembly.CoreAssembly
	reloads *reloadGate

	// set by phase5
	hh                   *health.Handler
	healthRouteGroupOpts []HealthRouteGroupOption            // resolved HealthRouteGroupOption stack (from WithHealthRoutes)
	rtr                  *router.Router                      // primary listener's router (may be nil when no primary)
	routers              map[cell.ListenerRef]*router.Router // all per-listener routers

	// registeredCheckers guards against duplicate health checker names across
	// phases. Keyed by checker name; value is struct{}.
	registeredCheckers map[string]struct{}
}

// newPhaseState creates a phaseState wrapping a fresh runState and returns
// the owned run context alongside. Callers that only need the state (e.g.
// teardown-only tests) may discard the context via blank identifier.
func newPhaseState() (context.Context, *phaseState) {
	runCtx, rs := newRunState()
	return runCtx, &phaseState{
		runState:           rs,
		registeredCheckers: make(map[string]struct{}),
	}
}

// registerHealthChecker adds a named readiness checker to hh, returning an
// error on duplicate names (instead of panicking like hh.RegisterChecker).
func (s *phaseState) registerHealthChecker(name string, fn func(context.Context) error) error {
	if _, exists := s.registeredCheckers[name]; exists {
		return fmt.Errorf("bootstrap: duplicate health checker %q", name)
	}
	if err := s.hh.RegisterChecker(name, fn); err != nil {
		return err
	}
	s.registeredCheckers[name] = struct{}{}
	return nil
}

// addCloser registers a resource for teardown, preferring kernellifecycle.ContextCloser
// over io.Closer so that the shared shutCtx budget flows through to the resource.
//
// Priority:
//  1. kernellifecycle.ContextCloser: Close(ctx) — budget propagated directly.
//  2. io.Closer: wrapped via kernellifecycle.IgnoreCtx (ctx discarded at boundary).
//  3. Neither: silently skipped.
//
// ref: uber-go/fx Lifecycle.Append — OnStop hook receives the shared StopTimeout ctx.
func (s *phaseState) addCloser(res any) {
	if res == nil {
		return
	}
	name := fmt.Sprintf("%T", res)
	if cc, ok := res.(kernellifecycle.ContextCloser); ok {
		s.addNamedTeardown(name, cc.Close)
		return
	}
	if ic, ok := res.(io.Closer); ok {
		// F20: io.Closer fallback — the shared shutCtx budget is NOT propagated
		// to this resource. All GoCell adapters implement ContextCloser; this
		// path is only reached by external or legacy resources.
		slog.Warn("bootstrap: resource registered as io.Closer only; shutdown budget will NOT apply",
			slog.String("type", name))
		s.addNamedTeardown(name, kernellifecycle.IgnoreCtx(ic).Close)
	}
	// else: resource has no Close method — silently skip.
}

// phase5BuildRouters builds one Router per declared listener, collects
// RouteGroups from all RouteGroupContributor cells, mounts each group on its
// listener's router, and finalizes auth on the primary router.
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
	if b.http.readyzDeadline > 0 {
		hhOpts = append(hhOpts, health.WithDeadline(b.http.readyzDeadline))
	}
	// PR-A35 + PR-A14b round-3: WithReadyzVerboseDisabled is a
	// HealthRouteGroupOption (no longer a bootstrap-level Option) — peek
	// at the resolved cfg to thread the health.Option to the handler.
	if cfg.verboseDisabled {
		hhOpts = append(hhOpts, health.WithVerboseDisabled())
	}
	hh := health.New(s.asm, hhOpts...)
	if b.http.adapterInfo != nil {
		hh.SetAdapterInfo(b.http.adapterInfo)
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
	s.healthRouteGroupOpts = b.http.healthRouteGroupOpts
	return b.registerAllHealthCheckers(s)
}

// phase5BuildPerListenerRouters creates one Router per declared listener config.
func (b *Bootstrap) phase5BuildPerListenerRouters(s *phaseState) (map[cell.ListenerRef]*router.Router, error) {
	routers := make(map[cell.ListenerRef]*router.Router, len(b.http.listenerConfigs))
	for ref, cfg := range b.http.listenerConfigs {
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

// phase5CollectRouteGroups collects RouteGroups from health and RouteGroupContributor cells.
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
		c := s.asm.Cell(id)
		if rgc, ok := c.(cell.RouteGroupContributor); ok {
			cellGroups := rgc.RouteGroups()
			for i := range cellGroups {
				cellGroups[i].CellID = id
			}
			groups = append(groups, cellGroups...)
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
		if err := b.mountOneRouteGroup(rtr, rg, i); err != nil {
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

// mountOneRouteGroup mounts a single RouteGroup on its router and applies any
// non-auth Middleware in declaration order. PR269 round-3: group-level auth is
// gone — auth scheme is a listener concern (cells that need a different scheme
// declare their routes on a different listener). Listener-level authChain is
// already installed by router.WithDefaultMiddleware; group Middleware runs
// after that chain at request time (chi sub-mux With order).
func (b *Bootstrap) mountOneRouteGroup(rtr *router.Router, rg cell.RouteGroup, _ int) error {
	register := rg.Register
	if len(rg.Middleware) > 0 {
		mws := rg.Middleware
		inner := register
		register = func(sub cell.RouteMux) error {
			return inner(sub.With(mws...))
		}
	}
	var registerErr error
	if rg.Prefix != "" {
		// rtr.Route signature is from cell.RouteMux which still takes a no-error
		// closure; capture the Register error via the outer variable so it
		// surfaces to the phase5 walker. rtr.Route is synchronous (chi.Mux
		// builds the sub-tree before returning) so registerErr is read after
		// the closure exits — no data race on the outer variable.
		rtr.Route(rg.Prefix, func(sub cell.RouteMux) {
			if err := register(sub); err != nil {
				registerErr = err
			}
		})
	} else {
		registerErr = register(rtr)
	}
	return registerErr
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
	cfg, ok := b.http.listenerConfigs[ref]
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
// listener are actually gated by an auth-flavoured plan chain. The acceptable
// plans are AuthJWT / AuthJWTFromAssembly, AuthMTLS, or AuthServiceToken (or
// any combination). AuthNone chains with protected routes (non-Public,
// non-Internal) cause Run() to fail-closed at phase5.
//
// Uses chainProtectsRoutes (from auth_plan_describe.go) for the typed check,
// replacing the old string-based isAuthFlavoredPolicy check.
func (b *Bootstrap) validateAuthVerifierForDeclaredRoutes(ref cell.ListenerRef, rtr *router.Router) error {
	cfg := b.http.listenerConfigs[ref]
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
func (b *Bootstrap) buildListenerRouterOpts(s *phaseState, ref cell.ListenerRef, cfg listenerConfig) ([]router.Option, error) {
	opts := make([]router.Option, 0, len(b.http.routerOpts)+6)
	opts = append(opts, b.http.routerOpts...)

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

// registerAllHealthCheckers registers option-supplied, cell-discovered, watcher,
// and drift health checkers. Returns error on duplicate names or nil checkers.
func (b *Bootstrap) registerAllHealthCheckers(s *phaseState) error {
	for _, hc := range b.http.healthCheckers {
		if err := s.registerHealthChecker(hc.name, hc.fn); err != nil {
			return err
		}
	}
	if err := b.registerCellHealthCheckers(s); err != nil {
		return err
	}
	if s.cfgWatcher != nil {
		cfgHealth := s.cfgWatcher.Health // func() error — wrap to ctx-aware signature
		if err := s.registerHealthChecker(configWatcherCheckerName, func(_ context.Context) error {
			return cfgHealth()
		}); err != nil {
			return err
		}
	}
	return b.registerConfigDriftChecker(s)
}

// registerCellHealthCheckers auto-discovers HealthContributor cells.
func (b *Bootstrap) registerCellHealthCheckers(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		hcc, ok := s.asm.Cell(id).(cell.HealthContributor)
		if !ok {
			continue
		}
		if err := b.registerOneCellHealthCheckers(s, id, hcc); err != nil {
			return err
		}
	}
	return nil
}

// registerOneCellHealthCheckers registers all health checkers from a single
// HealthContributor cell, in sorted order.
func (b *Bootstrap) registerOneCellHealthCheckers(s *phaseState, id string, hcc cell.HealthContributor) error {
	cellCheckers := hcc.HealthCheckers()
	names := make([]string, 0, len(cellCheckers))
	for k := range cellCheckers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		fn := cellCheckers[name]
		if fn == nil {
			return fmt.Errorf("bootstrap: cell %q returned nil health checker for %q", id, name)
		}
		if err := s.registerHealthChecker(name, fn); err != nil {
			return err
		}
	}
	return nil
}

// phase3bDiscoverLifecycleContributor auto-registers lifecycle hooks from all
// cells implementing cell.LifecycleContributor. Mirrors registerCellHealthCheckers
// (above) to keep the discovery pattern symmetric.
//
// Must run after phase3InitAssembly (cells need Init to have populated any
// state the hooks close over) and before b.lifecycle.Start(ctx).
//
// Cross-path uniqueness: Lifecycle.Append is the single source of truth for
// duplicate-Name detection (returns ErrDuplicateHookName). That guard covers
// every entry path into the shared Lifecycle — phase3b auto-discovery,
// WithLifecycle explicit registration, and any future callers — without
// needing a phase-local "seen" map that could drift from reality.
//
// ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go — Hook, Append ordering.
// ref: kernel/cell.HealthContributor — mirrored auto-discovery pattern.
func (b *Bootstrap) phase3bDiscoverLifecycleContributor(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		lc, ok := s.asm.Cell(id).(cell.LifecycleContributor)
		if !ok {
			continue
		}
		if err := b.registerOneCellLifecycleHooks(id, lc); err != nil {
			return err
		}
	}
	return nil
}

// registerOneCellLifecycleHooks appends the hooks from a single cell. Duplicate
// Name detection is delegated to Lifecycle.Append. Extracted from phase3b to
// keep cognitive complexity under the project ceiling.
func (b *Bootstrap) registerOneCellLifecycleHooks(id string, lc cell.LifecycleContributor) error {
	for _, h := range lc.LifecycleHooks() {
		if h.OnStart == nil && h.OnStop == nil {
			continue
		}
		if err := b.lc.kernel.Append(Hook{
			CellID:       id,
			Name:         h.Name,
			OnStart:      h.OnStart,
			OnStop:       h.OnStop,
			StartTimeout: h.StartTimeout,
			StopTimeout:  h.StopTimeout,
		}); err != nil {
			return fmt.Errorf("bootstrap: cell %q lifecycle hook %q: %w", id, h.Name, err)
		}
	}
	return nil
}

// registerConfigDriftChecker registers the config-drift health probe when the
// config supports generation tracking.
func (b *Bootstrap) registerConfigDriftChecker(s *phaseState) error {
	cfg := s.cfg
	g, gOK := cfg.(config.Generationer)
	og, ogOK := cfg.(config.ObservedGenerationer)
	if !gOK || !ogOK {
		return nil
	}
	return s.registerHealthChecker(configDriftCheckerName, func(_ context.Context) error {
		if config.HasDrift(cfg) {
			return fmt.Errorf("config drift: generation %d, observed %d",
				g.Generation(), og.ObservedGeneration())
		}
		return nil
	})
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
// ref: runtime/observability/metrics.NewProviderCollector — provider-neutral
// HTTP collector that records http_requests_total + http_request_duration_seconds.
func (b *Bootstrap) autoWireHTTPMetricsCollector(opts []router.Option) ([]router.Option, error) {
	if b.metrics.provider == nil {
		return opts, nil
	}
	// NopProvider is the default when no provider is injected; skip auto-wire
	// to avoid allocating a no-op collector on every bootstrap startup.
	if _, isNop := b.metrics.provider.(kernelmetrics.NopProvider); isNop {
		return opts, nil
	}
	// Create the collector only once (cached in b.metrics.httpCollector) so that
	// multiple calls to buildListenerRouterOpts (one per declared listener)
	// share the same collector and do not attempt to re-register Prometheus
	// counters/histograms with the same names.
	if b.metrics.httpCollector == nil {
		// Derive cell ID from the most specific source available:
		//  1. Explicit WithAssemblyID — caller's intent takes precedence.
		//  2. Pre-built assembly's ID (b.assembly.core.ID()) — avoids requiring callers
		//     to repeat the assembly ID when using WithAssembly(asm).
		//  3. Fallback "default" — matches the ID used by the auto-built assembly.
		cellID := b.assembly.assemblyID
		if cellID == "" && b.assembly.core != nil {
			cellID = b.assembly.core.ID()
		}
		if cellID == "" {
			cellID = "default"
		}
		collector, err := metricsmiddleware.NewProviderCollector(b.metrics.provider, metricsmiddleware.ProviderCollectorConfig{
			CellID: cellID,
		})
		if err != nil {
			return nil, fmt.Errorf(
				"bootstrap: metrics auto-wire conflict: WithMetricsProvider already constructs the HTTP collector; "+
					"do not also pass router.WithMetricsCollector via WithRouterOptions. "+
					"Remove one side: %w", err)
		}
		b.metrics.httpCollector = collector
	}
	return append(opts, router.WithMetricsCollector(b.metrics.httpCollector)), nil
}

// buildAuthRouterOptions assembles the auth-middleware and optional metrics
// options for the given verifier.
func (b *Bootstrap) buildAuthRouterOptions(v auth.IntentTokenVerifier) ([]router.Option, error) {
	opts := []router.Option{router.WithAuthMiddleware(v)}
	if b.metrics.provider == nil {
		return opts, nil
	}
	am, err := auth.NewAuthMetrics(b.metrics.provider)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: register auth metrics: %w", err)
	}
	opts = append(opts, router.WithAuthMetrics(am))
	return opts, nil
}

// phase6StartEventRouter registers subscriptions and starts the event router
// using state.runCtx (independent of the external context).
//
// Key invariant: evtRouter.Run uses state.runCtx, NOT the external ctx.
// External ctx cancellation triggers phase9 → phase10 which calls evtRouter.Close;
// that closes runCtx internally, causing Run to return.
// ref: uber-go/fx app.go:L545-567 (run vs stop ctx separation).
func (b *Bootstrap) phase6StartEventRouter(runCtx context.Context, s *phaseState) error {
	sub := s.sub
	if sub == nil {
		return b.checkNoEventRegistrars(s.asm)
	}

	// Observability context restoration is the OUTERMOST step inside
	// SubscriberWithMiddleware.Subscribe — built-in invariant, not a
	// middleware here. ContractTracingMiddleware therefore observes a
	// ctx already populated with entry.Observability fields.
	mws := []outbox.SubscriptionMiddleware{
		eventrouter.ContractTracingMiddleware(b.http.wrapperTracer, b.http.errorRedactor),
	}
	mws = append(mws, b.events.consumerMiddleware...)

	var evtRouterOpts []eventrouter.Option
	if b.events.routerReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.events.routerReadyTimeout))
	}
	evtRouter := eventrouter.New(&outbox.SubscriberWithMiddleware{
		Inner:      sub,
		Middleware: mws,
	}, evtRouterOpts...)

	for _, id := range s.asm.CellIDs() {
		c := s.asm.Cell(id)
		if er, ok := c.(cell.EventRegistrar); ok {
			if err := er.RegisterSubscriptions(evtRouter); err != nil {
				return fmt.Errorf("bootstrap: cell %s subscription setup failed: %w", id, err)
			}
		}
	}

	if evtRouter.HandlerCount() == 0 {
		return nil
	}

	evtHealth := evtRouter.Health // func() error — wrap to ctx-aware signature
	if err := s.registerHealthChecker(eventRouterCheckerName, func(_ context.Context) error {
		return evtHealth()
	}); err != nil {
		return err
	}

	slog.Info("bootstrap: starting event router",
		slog.Int("handler_count", evtRouter.HandlerCount()))

	routerErrCh := make(chan error, 1)
	// evtRouter.Run uses runCtx — not the external ctx.
	// ref: uber-go/fx run vs stop ctx separation.
	go func() {
		routerErrCh <- evtRouter.Run(runCtx)
	}()

	select {
	case err := <-routerErrCh:
		return fmt.Errorf("bootstrap: event router: %w", err)
	case <-evtRouter.Running():
		// All subscriptions consuming.
	}

	s.routerErrCh = routerErrCh
	s.addTeardown(func(c context.Context) error {
		return evtRouter.Close(c)
	})
	return nil
}

// checkNoEventRegistrars returns an error when any cell implements EventRegistrar
// but no subscriber is configured.
func (b *Bootstrap) checkNoEventRegistrars(asm *assembly.CoreAssembly) error {
	for _, id := range asm.CellIDs() {
		if _, ok := asm.Cell(id).(cell.EventRegistrar); ok {
			return fmt.Errorf(
				"bootstrap: cell %s implements EventRegistrar but no subscriber is configured; "+
					"add WithSubscriber to bootstrap options", id)
		}
	}
	return nil
}

// phase8StartWorkers starts the WorkerGroup using the caller-supplied runCtx
// (independent of external ctx). The workerCancel is only called inside the
// teardown closure so that worker.Stop is the trigger for cancellation during
// phase10.
//
// Key invariant: workerCtx derives from runCtx, NOT from the external ctx.
// ref: uber-go/fx run vs stop ctx separation.
func (b *Bootstrap) phase8StartWorkers(runCtx context.Context, s *phaseState) {
	wg := worker.NewWorkerGroup()
	for _, w := range b.events.workers {
		wg.Add(w)
	}

	// workerCtx derives from runCtx so external ctx cancel does NOT immediately
	// stop workers. Workers stop only when phase10 calls their teardown.
	workerCtx, workerCancel := context.WithCancel(runCtx)

	if len(b.events.workers) == 0 {
		workerCancel() // no workers; release immediately
		return
	}

	workerErrCh := make(chan error, 1)
	go func() {
		workerErrCh <- wg.Start(workerCtx)
		close(workerErrCh)
	}()

	s.workerErrCh = workerErrCh
	s.addTeardown(func(c context.Context) error {
		workerCancel()
		stopErr := wg.Stop(c)
		// Wait for the wg.Start goroutine to finish so that all worker goroutines
		// have fully exited before Run() returns.
		//
		// In the ctx-cancel path, phase9 never reads from workerErrCh, so the
		// goroutine is still blocked on wg.Wait(). We drain it here to prevent
		// goroutine leaks and races on state set inside worker goroutines (e.g.,
		// the workerCtxCancelledAt atomic in tests).
		//
		// In the worker-error path, phase9 already drained the error; the channel
		// is closed and this select returns immediately.
		select {
		case <-workerErrCh:
		case <-c.Done():
		}
		return stopErr
	})
}

// drainHTTPErrors collects the first error and any additional errors already
// buffered in ch, then joins them. Called only after receiving the first error
// from httpErrCh so the channel is guaranteed non-empty at entry.
func drainHTTPErrors(ch <-chan error, first error) error {
	allErrs := []error{first}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return errors.Join(allErrs...)
			}
			if e != nil {
				allErrs = append(allErrs, e)
			}
		default:
			return errors.Join(allErrs...)
		}
	}
}

// phase9AwaitShutdownSignal blocks until one of: external ctx cancel, HTTP error,
// worker error, or router error. It returns a shutdownSignal describing what fired.
// It does NOT cancel workerCtx or runCtx — that happens in phase10.
//
// CORR-04: after receiving the first HTTP error from httpErrCh, drain any remaining
// errors and join them so no error is silently discarded.
func (b *Bootstrap) phase9AwaitShutdownSignal(ctx context.Context, s *phaseState) shutdownSignal {
	slog.Info("bootstrap: application started successfully")
	select {
	case <-ctx.Done():
		slog.Info("bootstrap: context cancelled, shutting down")
		return shutdownSignal{reason: reasonCtxCancel}
	case firstErr := <-s.httpErrCh:
		return shutdownSignal{reason: reasonHTTPError, err: drainHTTPErrors(s.httpErrCh, firstErr)}
	case err := <-s.workerErrCh:
		if err != nil {
			slog.Error("bootstrap: worker failed, initiating shutdown", slog.Any("error", err))
		}
		return shutdownSignal{reason: reasonWorkerError, err: err}
	case err := <-s.routerErrCh:
		if err != nil {
			slog.Error("bootstrap: event router failed, initiating shutdown", slog.Any("error", err))
		}
		return shutdownSignal{reason: reasonRouterError, err: err}
	}
}

// phase10OrchestrateShutdown executes the three-stage LIFO shutdown:
//
//  1. Readiness flip (SetShuttingDown so LBs drain traffic)
//  2. Pre-shutdown delay (optional, shares the total shutdownTimeout budget)
//  3. LIFO teardown of all registered components
//
// If the incoming signal carries a non-nil error (HTTP/worker/router failure) AND
// phase10 teardown itself is clean, the signal error is still returned to the
// caller so Run() surfaces the triggering failure.
//
// ref: sigs.k8s.io/controller-runtime engageStopProcedure (LIFO + StopAndWait)
// ref: uber-go/fx app.go StopTimeout
// ref: Kubernetes pod shutdown model (preStop counts toward terminationGracePeriodSeconds)
func (b *Bootstrap) phase10OrchestrateShutdown(s *phaseState, sig shutdownSignal) error {
	shutCtx, cancel := context.WithTimeout(context.Background(), b.lc.shutdownTimeout)
	defer cancel()

	m := b.metrics.shutdownMet
	totalStart := time.Now()

	// --- stage 1: readiness flip ---
	m.recordPhaseEntry(shutdownPhaseReadinessFlip)
	flipStart := time.Now()
	b.phase10ReadinessFlip(shutCtx, s)
	m.observePhaseDuration(shutdownPhaseReadinessFlip, time.Since(flipStart))

	// --- stage 2: LIFO teardown ---
	m.recordPhaseEntry(shutdownPhaseLIFOTeardown)
	tearStart := time.Now()
	teardownErrs := b.phase10LIFOTeardown(shutCtx, s)
	m.observePhaseDuration(shutdownPhaseLIFOTeardown, time.Since(tearStart))

	// --- stage 3: finalize ---
	m.recordPhaseEntry(shutdownPhaseClosed)
	m.observePhaseDuration("total", time.Since(totalStart))

	// F3: outcome reflects the final return semantics, not just ctx state.
	// Precedence: timeout > teardown_error > signal_error > success.
	//   - timeout       : shutCtx expired during any stage; worst case for SREs.
	//   - teardown_error: at least one teardown returned non-nil (non-timeout).
	//   - signal_error  : shutdown was triggered by an HTTP/worker/router error,
	//                     teardown itself was clean.
	//   - success       : user-initiated shutdown with clean teardown.
	teardownErr := errors.Join(teardownErrs...)
	outcome := "success"
	switch {
	case shutCtx.Err() != nil:
		outcome = "timeout"
	case teardownErr != nil:
		outcome = "teardown_error"
	case sig.err != nil:
		outcome = "signal_error"
	}
	m.countOutcome(outcome)

	// Safety net: cancel runCtx after all teardowns complete so any goroutine
	// still holding runCtx eventually unblocks.
	s.runCancel()

	if teardownErr != nil {
		return teardownErr
	}
	// Surface the triggering signal error when teardown itself was clean.
	return sig.err
}

// phase10ReadinessFlip marks the health handler as shutting down (503) and
// waits for the preShutdownDelay, sharing the shutCtx budget.
func (b *Bootstrap) phase10ReadinessFlip(shutCtx context.Context, s *phaseState) {
	slog.Info("bootstrap: initiating graceful shutdown")
	if s.reloads != nil {
		// early signal: prevents new reload callbacks from entering the gate;
		// the returned drained channel is intentionally not awaited here.
		// Full drain (BeginShutdown + drain + ctx.Done) happens in the phase3
		// teardown closure registered in phase3InitAssembly, which executes
		// during phase10LIFOTeardown at the end of the shutdown sequence.
		s.reloads.BeginShutdown()
	}
	if s.hh != nil {
		s.hh.SetShuttingDown()
	}

	if b.lc.preShutdownDelay <= 0 {
		return
	}
	slog.Info("bootstrap: pre-shutdown drain delay",
		slog.Duration("delay", b.lc.preShutdownDelay))
	select {
	case <-time.After(b.lc.preShutdownDelay):
	case <-shutCtx.Done():
	}
}

// phase10LIFOTeardown runs all teardown functions in reverse registration order.
// Errors are collected but do not abort remaining teardowns (best-effort cleanup).
// Each non-nil error is wrapped in a phaseError with the component name so that
// post-mortem diagnosis can pinpoint the failing resource without trawling logs.
//
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go engageStopProcedure — LIFO.
func (b *Bootstrap) phase10LIFOTeardown(shutCtx context.Context, s *phaseState) []error {
	var errs []error
	for i := len(s.teardowns) - 1; i >= 0; i-- {
		td := s.teardowns[i]
		if err := td.fn(shutCtx); err != nil {
			if td.name != "" {
				err = &phaseError{Phase: "teardown_" + td.name, Err: err}
			}
			slog.Error("bootstrap: shutdown step failed",
				slog.String("phase", td.name),
				slog.Any("error", err))
			errs = append(errs, err)
		}
	}
	return errs
}
