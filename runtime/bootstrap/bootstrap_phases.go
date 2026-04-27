package bootstrap

// bootstrap_phases.go — phase method implementations for Bootstrap.Run().
//
// Each phase has cognitive complexity ≤ 15; Run() itself only calls phases.
//
// ref: uber-go/fx app.go (Run splits startup/shutdown via StartTimeout/StopTimeout)
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go (engageStopProcedure LIFO teardown)

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
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

// phase0ValidateOptions checks all option preconditions before any side effects.
// Returns immediately on the first violation so the error message is unambiguous.
func (b *Bootstrap) phase0ValidateOptions() error {
	// Surface shutdown metrics registration errors before any component starts.
	if b.shutdownMetricsErr != nil {
		return fmt.Errorf("bootstrap: shutdown metrics registration failed: %w", b.shutdownMetricsErr)
	}
	if err := b.validateHealthCheckers(); err != nil {
		return err
	}
	if b.circuitBreakerNil {
		return fmt.Errorf("bootstrap: circuit breaker must not be nil")
	}
	if b.managedResourceNil {
		return fmt.Errorf("bootstrap: managed resource must not be nil in WithManagedResource")
	}
	if err := b.validateAuthPlanAssemblyMatch(); err != nil {
		return err
	}
	if err := b.validateAuthPlanMTLSBindings(); err != nil {
		return err
	}
	if err := b.validateAuthChainJWTSingleton(); err != nil {
		return err
	}
	if err := b.validateAuthNoneExclusive(); err != nil {
		return err
	}
	if err := b.validateAuthServiceTokenPlans(); err != nil {
		return err
	}
	// PR-A14b: validate declarative listener configs last — other option
	// errors (nil checkers, nil resources, mutual exclusion) are option-level
	// mistakes and should surface before HTTP-layout errors.
	if err := b.validateHTTPListenerConfigs(); err != nil {
		return err
	}
	return nil
}

// validateHealthCheckers ensures every caller-registered checker has a non-
// empty unique name and a non-nil callback. Duplicates silently shadow each
// other in the health handler, making one probe invisible — surface the
// error at phase 0 before any side effects.
func (b *Bootstrap) validateHealthCheckers() error {
	seen := make(map[string]struct{}, len(b.healthCheckers))
	for _, hc := range b.healthCheckers {
		if hc.name == "" {
			return fmt.Errorf("bootstrap: health checker name must not be empty")
		}
		if hc.fn == nil {
			return fmt.Errorf("bootstrap: health checker %q must not be nil", hc.name)
		}
		if _, dup := seen[hc.name]; dup {
			return fmt.Errorf("bootstrap: duplicate health checker name %q; each checker must have a unique name", hc.name)
		}
		seen[hc.name] = struct{}{}
	}
	return nil
}

// phase1LoadConfig loads configuration, creates the config watcher (if a path
// was provided), and registers closable middleware teardowns.
// On success, s.cfg and s.cfgWatcher are populated.
func (b *Bootstrap) phase1LoadConfig(s *phaseState) error {
	if b.configPath != "" {
		cfg, err := config.Load(b.configPath, b.envPrefix)
		if err != nil {
			return fmt.Errorf("bootstrap: load config: %w", err)
		}
		s.cfg = cfg
	} else {
		s.cfg = config.NewFromMap(make(map[string]any))
	}

	// Create the watcher but do NOT start it yet — OnChange must be bound first
	// (phase4) to prevent a window where file events are lost.
	if b.configPath != "" {
		w, err := b.configWatcherFactory(b.configPath)
		if err != nil {
			return fmt.Errorf("bootstrap: config watcher: %w", err)
		}
		s.cfgWatcher = w
		// Watcher.CloseCtx propagates the shared shutCtx budget to the
		// in-flight callback drain phase (replaces the fixed drainTimeout).
		s.addCloser(s.cfgWatcher)
	}

	// Register closable middleware dependencies (e.g. rate limiter background
	// goroutines). addCloser prefers ContextCloser over io.Closer so that
	// resources upgraded to CloseCtx automatically receive the shut budget.
	for _, cl := range b.closers {
		s.addCloser(cl)
	}

	// Tracer wiring: b.wrapperTracer is threaded into router.WithTracer
	// (phase7, HTTP side) and ContractTracingMiddleware (phase6, consumer side)
	// at the construction call sites. When WithTracer was not supplied,
	// HTTP tracing is disabled and wrapper.WrapConsumer falls back to
	// NoopTracer so spans degrade silently — no package-level setup needed.
	if b.wrapperTracer == nil {
		slog.Warn("bootstrap: no tracer provided, HTTP tracing is disabled and consumer spans will be no-op; use WithTracer to enable distributed tracing")
	}
	return nil
}

// phase2InitPubSub initialises the publisher and subscriber.
// When neither is provided a default InMemoryEventBus serves both roles.
func (b *Bootstrap) phase2InitPubSub(s *phaseState) {
	pub := b.publisher
	sub := b.subscriber
	if pub == nil && sub == nil {
		eb := eventbus.New()
		pub = eb
		sub = eb
	}
	s.pub = pub
	s.sub = sub

	// outbox.Subscriber.Close now accepts ctx — use it directly so the teardown
	// passes the shared shutCtx budget through to the implementation.
	if sub != nil {
		s.addTeardown(sub.Close)
	}
	// Avoid double-close when pub and sub are the same instance.
	if pub != nil && !samePubSubIdentity(pub, sub) {
		s.addTeardown(pub.Close)
	}
}

func samePubSubIdentity(pub outbox.Publisher, sub outbox.Subscriber) bool {
	if pub == nil || sub == nil {
		return false
	}
	pubType := reflect.TypeOf(pub)
	if pubType != reflect.TypeOf(sub) || !pubType.Comparable() {
		return false
	}
	return any(pub) == any(sub)
}

// phase3InitAssembly builds (or reuses) the CoreAssembly, registers its LIFO
// teardown, then calls StartWithConfig.
// On success, s.asm and s.reloads are populated.
func (b *Bootstrap) phase3InitAssembly(ctx context.Context, s *phaseState) error {
	asm := b.assembly
	if asm == nil {
		cfg := assembly.Config{ID: "default", DurabilityMode: cell.DurabilityDemo}
		if b.hookTimeoutSet {
			cfg.HookTimeout = b.hookTimeout
		}
		if b.hookObserver != nil {
			cfg.HookObserver = b.hookObserver
		}
		if b.metricsProvider != nil {
			cfg.MetricsProvider = b.metricsProvider
		}
		asm = assembly.New(cfg)
	} else if b.hookTimeoutSet || b.hookObserver != nil {
		slog.Warn("bootstrap: WithHookTimeout/WithHookObserver ignored because WithAssembly was used; configure via assembly.Config")
	}

	// Register Shutdown BEFORE StartWithConfig: CoreAssembly.New eagerly spawns
	// the hook-dispatcher goroutine. If Start fails the goroutine stays alive
	// until Shutdown/Stop is called. Rollback LIFO ensures it is reached.
	// ref: uber-go/fx app.go Rollback — register every component before starting.
	s.addTeardown(func(_ context.Context) error {
		asm.Shutdown()
		return nil
	})

	cfgMap := snapshotConfig(s.cfg)
	if err := asm.StartWithConfig(ctx, cfgMap); err != nil {
		return fmt.Errorf("bootstrap: assembly start: %w", err)
	}

	s.asm = asm

	// reloads gates config-reload callbacks: reject new entries once shutdown
	// begins; drain in-flight before stopping the assembly.
	// ref: net/http Server.Shutdown — stop accepting + drain + close.
	reloads := newReloadGate()
	s.reloads = reloads

	s.addTeardown(func(c context.Context) error {
		drained := reloads.BeginShutdown()
		select {
		case <-drained:
		case <-c.Done():
			return c.Err()
		}
		return asm.Stop(c)
	})
	return nil
}

// phase4WireAuthAndWatcher discovers JWT verifiers from authProvider cells for
// any AuthJWTFromAssembly plan (writes the resolved verifier into the plan's
// atomic.Pointer), then binds the config-watcher OnChange callback.
func (b *Bootstrap) phase4WireAuthAndWatcher(s *phaseState) error {
	if err := b.runAuthPlanValidateHooks(); err != nil {
		return err
	}
	b.bindConfigWatcher(s)
	return nil
}

// bindConfigWatcher registers OnChange and starts the watcher (if one was created).
func (b *Bootstrap) bindConfigWatcher(s *phaseState) {
	if s.cfgWatcher == nil {
		return
	}
	yamlPath, envPrefix := b.configPath, b.envPrefix
	s.cfgWatcher.OnChange(b.buildOnChangeCallback(s, yamlPath, envPrefix))
	// Start after OnChange is bound so no events are consumed without a handler.
	s.cfgWatcher.Start()
}

// buildOnChangeCallback creates the config-watcher callback closure.
// Extracted to keep bindConfigWatcher's cognitive complexity ≤ 15.
func (b *Bootstrap) buildOnChangeCallback(s *phaseState, yamlPath, envPrefix string) func(config.WatchEvent) {
	return func(evt config.WatchEvent) {
		if !s.reloads.TryEnter() {
			slog.Warn("bootstrap: config reload rejected during shutdown",
				slog.String("path", evt.Path))
			return
		}
		defer s.reloads.Leave()
		b.applyConfigReload(s, evt, yamlPath, envPrefix)
	}
}

// applyConfigReload performs the actual config reload: Reload → Diff → notify cells.
// Extracted to keep buildOnChangeCallback's cognitive complexity ≤ 15.
func (b *Bootstrap) applyConfigReload(s *phaseState, evt config.WatchEvent, yamlPath, envPrefix string) {
	rc, ok := s.cfg.(config.Reloader)
	if !ok {
		return
	}
	oldSnap := snapshotConfig(s.cfg)
	if err := rc.Reload(yamlPath, envPrefix); err != nil {
		slog.Error("bootstrap: config reload failed", slog.Any("error", err))
		return
	}
	slog.Info("bootstrap: config reloaded", slog.String("path", evt.Path))

	newSnap := snapshotConfig(s.cfg)
	added, updated, removed := config.Diff(oldSnap, newSnap)
	if len(added) == 0 && len(updated) == 0 && len(removed) == 0 {
		slog.Debug("bootstrap: config reloaded but no effective changes")
		syncObservedGeneration(s.cfg)
		return
	}

	var gen int64
	if g, ok := s.cfg.(config.Generationer); ok {
		gen = g.Generation()
	}
	allOK := b.notifyCellsConfigChanged(s.asm, newSnap, added, updated, removed, gen)
	if allOK {
		if og, ok := s.cfg.(config.ObservedGenerationer); ok {
			og.SetObservedGeneration(gen)
		}
	}
}

// syncObservedGeneration aligns ObservedGeneration with Generation when there are no
// effective changes, preventing false config-drift alarms.
func syncObservedGeneration(cfg config.Config) {
	if og, ok := cfg.(config.ObservedGenerationer); ok {
		if g, gOK := cfg.(config.Generationer); gOK {
			og.SetObservedGeneration(g.Generation())
		}
	}
}

// notifyCellsConfigChanged notifies all ConfigReloader cells about a config change.
// Returns true only when every cell applied the change successfully.
func (b *Bootstrap) notifyCellsConfigChanged(
	asm *assembly.CoreAssembly,
	newSnap map[string]any,
	added, updated, removed []string,
	gen int64,
) bool {
	allOK := true
	for _, id := range asm.CellIDs() {
		c := asm.Cell(id)
		cr, ok := c.(cell.ConfigReloader)
		if !ok {
			continue
		}
		if !b.shouldNotifyCell(c, added, updated, removed) {
			continue
		}
		cfgSnap := b.buildCellConfigSnap(c, newSnap)
		evt := cell.ConfigChangeEvent{
			Added:      cloneStrings(added),
			Updated:    cloneStrings(updated),
			Removed:    cloneStrings(removed),
			Config:     cfgSnap,
			Generation: gen,
		}
		if !b.invokeCellReload(id, cr, evt) {
			allOK = false
		}
	}
	return allOK
}

// shouldNotifyCell returns true when a cell has no key-filter or one of the
// changed keys matches the declared prefixes.
func (b *Bootstrap) shouldNotifyCell(c cell.Cell, added, updated, removed []string) bool {
	kf, ok := c.(cell.ConfigKeyFilterer)
	if !ok {
		return true
	}
	prefixes := kf.ConfigKeyPrefixes()
	if len(prefixes) == 0 {
		return true
	}
	changedKeys := make([]string, 0, len(added)+len(updated)+len(removed))
	changedKeys = append(changedKeys, added...)
	changedKeys = append(changedKeys, updated...)
	changedKeys = append(changedKeys, removed...)
	return config.NewKeyFilter(prefixes...).Matches(changedKeys)
}

// buildCellConfigSnap constructs the config snapshot for a single cell,
// optionally filtered to the declared key prefixes.
func (b *Bootstrap) buildCellConfigSnap(c cell.Cell, newSnap map[string]any) map[string]any {
	snap := cloneMap(newSnap)
	kf, ok := c.(cell.ConfigKeyFilterer)
	if !ok {
		return snap
	}
	prefixes := kf.ConfigKeyPrefixes()
	if len(prefixes) > 0 {
		snap = filterMapByPrefixes(snap, prefixes)
	}
	return snap
}

// invokeCellReload calls cr.OnConfigReload inside a recover fence.
// Returns true on success.
func (b *Bootstrap) invokeCellReload(id string, cr cell.ConfigReloader, evt cell.ConfigChangeEvent) (ok bool) {
	ok = true
	func() {
		defer func() {
			if r := recover(); r != nil {
				ok = false
				slog.Error("bootstrap: config reload callback panic",
					slog.String("cell", id),
					slog.String("type", fmt.Sprintf("%T", r)))
				slog.Debug("bootstrap: config reload callback panic detail",
					slog.String("cell", id), slog.Any("panic", r))
			}
		}()
		if err := cr.OnConfigReload(evt); err != nil {
			ok = false
			slog.Error("bootstrap: config reload callback failed",
				slog.String("cell", id),
				slog.Any("error", err),
				slog.Int64("config_generation", evt.Generation))
		}
	}()
	return ok
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
	if b.readyzDeadline > 0 {
		hhOpts = append(hhOpts, health.WithDeadline(b.readyzDeadline))
	}
	// PR-A35 + PR-A14b round-3: WithReadyzVerboseDisabled is a
	// HealthRouteGroupOption (no longer a bootstrap-level Option) — peek
	// at the resolved cfg to thread the health.Option to the handler.
	if cfg.verboseDisabled {
		hhOpts = append(hhOpts, health.WithVerboseDisabled())
	}
	hh := health.New(s.asm, hhOpts...)
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
			"set bootstrap.WithListener(cell.InternalListener, ..., []cell.ListenerAuth{cell.NewAuthServiceToken(store, ring)}) "+
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
func (b *Bootstrap) buildListenerRouterOpts(s *phaseState, ref cell.ListenerRef, cfg listenerConfig) ([]router.Option, error) {
	opts := make([]router.Option, 0, len(b.routerOpts)+6)
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

// registerAllHealthCheckers registers option-supplied, cell-discovered, watcher,
// and drift health checkers. Returns error on duplicate names or nil checkers.
func (b *Bootstrap) registerAllHealthCheckers(s *phaseState) error {
	for _, hc := range b.healthCheckers {
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
		if err := b.lifecycle.Append(Hook{
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
		// Derive cell ID from the most specific source available:
		//  1. Explicit WithAssemblyID — caller's intent takes precedence.
		//  2. Pre-built assembly's ID (b.assembly.ID()) — avoids requiring callers
		//     to repeat the assembly ID when using WithAssembly(asm).
		//  3. Fallback "default" — matches the ID used by the auto-built assembly.
		cellID := b.assemblyID
		if cellID == "" && b.assembly != nil {
			cellID = b.assembly.ID()
		}
		if cellID == "" {
			cellID = "default"
		}
		collector, err := metricsmiddleware.NewProviderCollector(b.metricsProvider, metricsmiddleware.ProviderCollectorConfig{
			CellID: cellID,
		})
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
	opts := []router.Option{router.WithAuthMiddleware(v)}
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
		eventrouter.ContractTracingMiddleware(b.wrapperTracer, b.errorRedactor),
	}
	mws = append(mws, b.consumerMiddleware...)

	var evtRouterOpts []eventrouter.Option
	if b.eventRouterReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.eventRouterReadyTimeout))
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

// phase7StartHTTPServer creates and starts N http.Servers — one per declared
// listener — pre-binding sockets synchronously so port conflicts surface
// before any goroutine is started. Bind failures for listeners after the
// first are rolled back by closing already-owned sockets. Each server runs
// in its own goroutine; errors fan into a single buffered channel read by
// phase9. The LIFO teardown drains all servers in parallel under the shared
// shutCtx budget.
//
// PR-A14b: replaces the hard-coded primary+internal pair with a generic
// N-server loop driven by b.listenerConfigs.
//
// ref: go-kratos/kratos app.go@main L95-122 — per-server goroutine pair.
// ref: kubernetes/apiserver pkg/server/secure_serving.go — pre-bind listener.
// shutdownCtxFor derives the per-server shutdown context from the parent ctx
// and the listener's shutGrace setting.
//
//   - shutGrace > 0 wraps the parent with context.WithTimeout(parent, shutGrace).
//     context.WithTimeout already bounds the resulting deadline to whichever of
//     parent.Deadline() and (now + shutGrace) comes first, so the global
//     shutdownTimeout always wins when shutGrace exceeds it (R2-03).
//   - shutGrace == 0 returns the parent unchanged (no per-listener override; the
//     server inherits the global shutdownTimeout). The returned cancel is a
//     no-op so callers can defer it unconditionally.
func shutdownCtxFor(parent context.Context, shutGrace time.Duration) (context.Context, context.CancelFunc) {
	if shutGrace > 0 {
		return context.WithTimeout(parent, shutGrace)
	}
	return parent, noopShutdownCancel
}

// noopShutdownCancel is the cancel function returned by shutdownCtxFor when
// shutGrace == 0 — there is nothing to cancel because we did not derive a new
// context, but callers always defer the returned cancel so we hand back a
// no-op rather than a nil that would NPE.
func noopShutdownCancel() {}

// boundServer holds a resolved HTTP server and its associated listener.
type boundServer struct {
	name      string
	srv       *http.Server
	ln        net.Listener
	owned     bool          // true when bootstrap bound the socket (not caller-injected)
	shutGrace time.Duration // 0 means inherit the global shutdownTimeout
	authDesc  string        // OPS-09: auth chain description for startup log
}

func (b *Bootstrap) phase7StartHTTPServer(s *phaseState) error {
	servers, err := b.phase7BindListeners(s)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	httpErrCh := b.phase7ServeAll(servers)
	s.httpErrCh = httpErrCh
	// Convert to shutdownTasks for teardown (OPS-01: named logging via shutdownTask.name).
	shutTasks := boundServersToTasks(servers)
	s.addTeardown(func(c context.Context) error {
		return shutdownAllServers(c, shutTasks)
	})
	return nil
}

// phase7BindListeners pre-binds all declared listeners synchronously. If any
// bind fails, already-owned sockets are closed before returning the error.
//
// CORR-06: sort by ref.String() so bind order is deterministic across runs
// and log lines appear in a consistent order for operators.
// SEC-11: http.Server is constructed with ReadTimeout, WriteTimeout, and
// IdleTimeout to prevent Slowloris / slow-write DoS attacks.
// OPS-06: emit slog.Info after each successful bind (listener + addr + auth).
// OPS-07: emit slog.Warn when a non-loopback listener binds with AuthNone or empty auth chain.
func (b *Bootstrap) phase7BindListeners(s *phaseState) ([]boundServer, error) {
	// Collect and sort refs for deterministic iteration order.
	refs := make([]cell.ListenerRef, 0, len(b.listenerConfigs))
	for ref := range b.listenerConfigs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	var servers []boundServer
	for _, ref := range refs {
		cfg := b.listenerConfigs[ref]
		rtr, ok := s.routers[ref]
		if !ok {
			return nil, fmt.Errorf("bootstrap: no router for listener %q", ref.String())
		}
		ln, owned, err := resolveListener(cfg)
		if err != nil {
			closeOwnedSockets(servers)
			slog.Error("bootstrap: failed to bind HTTP listener",
				slog.String("listener", ref.String()),
				slog.String("addr", cfg.addr),
				slog.Any("error", err))
			return nil, fmt.Errorf("bootstrap: listen %s %s: %w", ref.String(), cfg.addr, err)
		}

		authDesc := describeAuthChain(cfg.authChain)
		slog.Info("bootstrap: HTTP listener bound",
			slog.String("listener", ref.String()),
			slog.String("addr", ln.Addr().String()),
			slog.String("auth", authDesc))

		// OPS-07 / F7 round-3: warn when a non-loopback address is served with
		// AuthNone or an empty auth chain. The dangerous case is the wildcard bind
		// (0.0.0.0 / ::): the listener is reachable on every interface, including
		// externally routable addresses, but ServeHTTP runs without an auth gate.
		// explicit_auth_none=true means the caller deliberately passed AuthNone{};
		// false means the chain was nil/empty (possibly an omission).
		if authDesc == "none" || authDesc == "" {
			if tcpAddr, ok2 := ln.Addr().(*net.TCPAddr); ok2 && !tcpAddr.IP.IsLoopback() {
				slog.Warn("bootstrap: listener bound to non-loopback address without auth; ensure network-level isolation",
					slog.String("listener", ref.String()),
					slog.String("addr", ln.Addr().String()),
					slog.Bool("wildcard_bind", tcpAddr.IP.IsUnspecified()),
					slog.Bool("explicit_auth_none", explicitAuthNone(cfg.authChain)))
			}
		}

		servers = append(servers, boundServer{
			name: ref.String(),
			srv: &http.Server{
				Handler:           rtr.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       60 * time.Second,
			},
			ln:        ln,
			owned:     owned,
			shutGrace: cfg.shutGrace,
			authDesc:  authDesc, // OPS-09: passed to phase7ServeAll startup log
		})
	}
	return servers, nil
}

// closeOwnedSockets closes all sockets that bootstrap owns (i.e. not caller-injected).
func closeOwnedSockets(servers []boundServer) {
	for _, prev := range servers {
		if prev.owned {
			_ = prev.ln.Close()
		}
	}
}

// phase7ServeAll starts all servers in background goroutines and returns a channel
// that receives errors and is closed when all servers have stopped.
func (b *Bootstrap) phase7ServeAll(servers []boundServer) chan error {
	n := len(servers)
	httpErrCh := make(chan error, n)
	pending := int32(n)
	for _, bs := range servers {
		bs := bs // capture
		go func() {
			slog.Info("bootstrap: HTTP server starting",
				slog.String("listener", bs.name),
				slog.String("addr", bs.ln.Addr().String()),
				slog.String("auth", bs.authDesc))
			err := bs.srv.Serve(bs.ln)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErrCh <- fmt.Errorf("%s listener: %w", bs.name, err)
			}
			if atomic.AddInt32(&pending, -1) == 0 {
				close(httpErrCh)
			}
		}()
	}
	return httpErrCh
}

// resolveListener returns the net.Listener for a listenerConfig. When
// cfg.net is set (caller-injected), it is returned directly (owned=false).
// Otherwise a new TCP socket is bound to cfg.addr (owned=true).
//
// CORR-03: when cfg.tls is non-nil, the TCP listener is wrapped with
// tls.NewListener so that TLS termination happens at the listener level.
// The pre-bound caller-injected net listener (cfg.net) is returned as-is
// since callers are expected to handle TLS on their own pre-bound socket.
func resolveListener(cfg listenerConfig) (ln net.Listener, owned bool, err error) {
	if cfg.net != nil {
		return cfg.net, false, nil
	}
	tcpLn, listenErr := net.Listen("tcp", cfg.addr)
	if listenErr != nil {
		return nil, false, listenErr
	}
	if cfg.tls != nil {
		return tls.NewListener(tcpLn, cfg.tls), true, nil
	}
	return tcpLn, true, nil
}

// shutdownTask represents a single server shutdown operation with its name and
// grace period. Extracted from boundServer to allow tests to inject arbitrary
// shutdown functions without requiring a real http.Server.
type shutdownTask struct {
	name      string
	shutGrace time.Duration
	shutdown  func(context.Context) error
}

// shutdownAllServers drains all servers in parallel. Each task uses a
// per-server context derived from the parent ctx:
//   - when shutGrace > 0 (set via WithListenerShutdownGrace), the parent ctx
//     is wrapped with context.WithTimeout(ctx, shutGrace) so shutGrace is an
//     upper bound *within* the global shutdownTimeout budget — never a parallel
//     budget that can outlive global shutdown.
//   - when shutGrace == 0, the shared ctx is passed through unchanged (server
//     inherits the global shutdownTimeout deadline).
//
// Errors are aggregated via errors.Join so operators see every failure.
//
// OPS-01: each task carries a name so shutdown log lines identify the listener.
// R2-03: parent ctx (not context.Background) is the timeout parent so the
// global shutdownTimeout always bounds per-listener drains.
func shutdownAllServers(ctx context.Context, tasks []shutdownTask) error {
	slog.Info("bootstrap: draining HTTP servers")
	type drainResult struct {
		err error
	}
	resultCh := make(chan drainResult, len(tasks))
	for _, task := range tasks {
		task := task // capture
		go func() {
			shutCtx, cancel := shutdownCtxFor(ctx, task.shutGrace)
			defer cancel()
			err := task.shutdown(shutCtx)
			if err != nil {
				slog.Error("bootstrap: HTTP server drain failed",
					slog.String("listener", task.name), slog.Any("error", err))
				// OPS-01: wrap with the listener name so the returned error chain
				// preserves the same attribution that already lives in the slog
				// line. Without this, errors.Join collapses to opaque "shutdown
				// failed" lines once the goroutine returns and operators can no
				// longer tell which listener tripped from the error object alone.
				err = fmt.Errorf("listener %q shutdown: %w", task.name, err)
			} else {
				slog.Info("bootstrap: HTTP server drained", slog.String("listener", task.name))
			}
			resultCh <- drainResult{err: err}
		}()
	}
	var errs []error
	for range tasks {
		r := <-resultCh
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	return errors.Join(errs...)
}

// boundServersToTasks converts []boundServer into []shutdownTask for shutdownAllServers.
func boundServersToTasks(servers []boundServer) []shutdownTask {
	tasks := make([]shutdownTask, len(servers))
	for i, bs := range servers {
		tasks[i] = shutdownTask{
			name:      bs.name,
			shutGrace: bs.shutGrace,
			shutdown:  bs.srv.Shutdown,
		}
	}
	return tasks
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
	for _, w := range b.workers {
		wg.Add(w)
	}

	// workerCtx derives from runCtx so external ctx cancel does NOT immediately
	// stop workers. Workers stop only when phase10 calls their teardown.
	workerCtx, workerCancel := context.WithCancel(runCtx)

	if len(b.workers) == 0 {
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
	shutCtx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer cancel()

	m := b.shutdownMet
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

	if b.preShutdownDelay <= 0 {
		return
	}
	slog.Info("bootstrap: pre-shutdown drain delay",
		slog.Duration("delay", b.preShutdownDelay))
	select {
	case <-time.After(b.preShutdownDelay):
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
