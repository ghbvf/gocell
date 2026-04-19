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
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/worker"
)

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
	hh  *health.Handler
	rtr *router.Router

	// registeredCheckers guards against duplicate health checker names across
	// phases. Keyed by checker name; value is struct{}.
	registeredCheckers map[string]struct{}
}

// newPhaseState creates a phaseState wrapping a fresh runState.
func newPhaseState() *phaseState {
	return &phaseState{
		runState:           newRunState(),
		registeredCheckers: make(map[string]struct{}),
	}
}

// registerHealthChecker adds a named readiness checker to hh, returning an
// error on duplicate names (instead of panicking like hh.RegisterChecker).
func (s *phaseState) registerHealthChecker(name string, fn func() error) error {
	if _, exists := s.registeredCheckers[name]; exists {
		return fmt.Errorf("bootstrap: duplicate health checker %q", name)
	}
	s.hh.RegisterChecker(name, health.Checker(fn))
	s.registeredCheckers[name] = struct{}{}
	return nil
}

// phase0ValidateOptions checks all option preconditions before any side effects.
// Returns immediately on the first violation so the error message is unambiguous.
func (b *Bootstrap) phase0ValidateOptions() error {
	// Surface shutdown metrics registration errors before any component starts.
	if b.shutdownMetricsErr != nil {
		return fmt.Errorf("bootstrap: shutdown metrics registration failed: %w", b.shutdownMetricsErr)
	}
	for _, hc := range b.healthCheckers {
		if hc.name == "" {
			return fmt.Errorf("bootstrap: health checker name must not be empty")
		}
		if hc.fn == nil {
			return fmt.Errorf("bootstrap: health checker %q must not be nil", hc.name)
		}
	}
	if b.circuitBreakerNil {
		return fmt.Errorf("bootstrap: circuit breaker must not be nil")
	}
	if b.brokerHealthNil {
		return fmt.Errorf("bootstrap: broker health checker must not be nil")
	}
	if err := b.validateInternalGuard(); err != nil {
		return err
	}
	if b.relayHealthNil {
		return fmt.Errorf("bootstrap: relay must not be nil in WithRelayHealth")
	}
	if b.authVerifier != nil && b.authDiscovery {
		return fmt.Errorf("bootstrap: WithAuthMiddleware and WithPublicEndpoints " +
			"are mutually exclusive; use WithPublicEndpoints (recommended)")
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
		s.addTeardown(func(_ context.Context) error {
			return s.cfgWatcher.Close()
		})
	}

	// Register closable middleware dependencies (e.g. rate limiter background goroutines).
	for _, cl := range b.closers {
		cl := cl // capture for closure
		s.addTeardown(func(_ context.Context) error {
			return cl.Close()
		})
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

	if cl, ok := sub.(io.Closer); ok {
		s.addTeardown(func(_ context.Context) error {
			return cl.Close()
		})
	}
	// Avoid double-close when pub and sub are the same instance.
	if cl, ok := pub.(io.Closer); ok && any(pub) != any(sub) {
		s.addTeardown(func(_ context.Context) error {
			return cl.Close()
		})
	}
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

// phase4WireAuthAndWatcher discovers the auth verifier (if needed) and binds
// the config-watcher OnChange callback, then starts the watcher.
func (b *Bootstrap) phase4WireAuthAndWatcher(s *phaseState) error {
	if err := b.discoverAuthVerifier(s); err != nil {
		return err
	}
	b.bindConfigWatcher(s)
	return nil
}

// discoverAuthVerifier populates b.authVerifier from cells when authDiscovery mode is on.
func (b *Bootstrap) discoverAuthVerifier(s *phaseState) error {
	if b.authVerifier != nil || !b.authDiscovery {
		return nil
	}
	var discoveredFrom string
	for _, id := range s.asm.CellIDs() {
		if ap, ok := s.asm.Cell(id).(authProvider); ok {
			if v := ap.TokenVerifier(); v != nil {
				if discoveredFrom != "" {
					return fmt.Errorf(
						"bootstrap: multiple auth provider cells discovered: %q and %q; use WithAuthMiddleware to select explicitly",
						discoveredFrom, id)
				}
				b.authVerifier = v
				discoveredFrom = id
			}
		}
	}
	if b.authVerifier == nil {
		return fmt.Errorf("bootstrap: WithPublicEndpoints requires an auth provider cell, but none was discovered")
	}
	slog.Info("bootstrap: auth verifier discovered from cell",
		slog.String("cell", discoveredFrom))
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

// phase5BuildHTTPRouter builds the HTTP router, registers health checkers,
// registers cell HTTP routes, and sets s.hh and s.rtr.
func (b *Bootstrap) phase5BuildHTTPRouter(s *phaseState) error {
	hh := health.New(s.asm)
	if b.adapterInfo != nil {
		hh.SetAdapterInfo(b.adapterInfo)
	}
	if b.verboseToken != "" {
		hh.SetVerboseToken(b.verboseToken)
	}
	s.hh = hh

	if err := b.registerAllHealthCheckers(s); err != nil {
		return err
	}

	rtr, err := b.buildRouter(s, hh)
	if err != nil {
		return err
	}

	// Register HTTP routes for all HTTPRegistrar cells.
	for _, id := range s.asm.CellIDs() {
		c := s.asm.Cell(id)
		if hr, ok := c.(cell.HTTPRegistrar); ok {
			hr.RegisterRoutes(rtr)
		}
	}

	s.rtr = rtr
	return nil
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
		if err := s.registerHealthChecker(configWatcherCheckerName, s.cfgWatcher.Health); err != nil {
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

// registerConfigDriftChecker registers the config-drift health probe when the
// config supports generation tracking.
func (b *Bootstrap) registerConfigDriftChecker(s *phaseState) error {
	cfg := s.cfg
	g, gOK := cfg.(config.Generationer)
	og, ogOK := cfg.(config.ObservedGenerationer)
	if !gOK || !ogOK {
		return nil
	}
	return s.registerHealthChecker(configDriftCheckerName, func() error {
		if config.HasDrift(cfg) {
			return fmt.Errorf("config drift: generation %d, observed %d",
				g.Generation(), og.ObservedGeneration())
		}
		return nil
	})
}

// buildRouter assembles all router options and calls router.NewE.
func (b *Bootstrap) buildRouter(s *phaseState, hh *health.Handler) (*router.Router, error) {
	opts := make([]router.Option, 0, len(b.routerOpts)+6)
	opts = append(opts, b.routerOpts...)
	if len(b.authPublicEndpoints) > 0 {
		opts = append(opts, router.WithPublicEndpoints(b.authPublicEndpoints))
	}
	if len(b.passwordResetExemptEndpoints) > 0 {
		opts = append(opts, router.WithPasswordResetExemptEndpoints(b.passwordResetExemptEndpoints))
	}
	if b.passwordResetChangeEndpointHint != "" {
		opts = append(opts, router.WithPasswordResetChangeEndpointHint(b.passwordResetChangeEndpointHint))
	}
	if b.authVerifier != nil {
		authOpts, err := b.buildAuthRouterOptions(s)
		if err != nil {
			return nil, err
		}
		opts = append(opts, authOpts...)
	}
	if b.internalGuard != nil {
		opts = append(opts, router.WithInternalPathPrefixGuard(b.internalGuardPrefix, b.internalGuard))
	}
	opts = append(opts, router.WithHealthHandler(hh))
	rtr, err := router.NewE(opts...)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	return rtr, nil
}

// buildAuthRouterOptions assembles the auth-middleware and optional metrics options.
func (b *Bootstrap) buildAuthRouterOptions(s *phaseState) ([]router.Option, error) {
	opts := []router.Option{
		router.WithAuthMiddleware(b.authVerifier, b.authPublicEndpoints),
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

// phase6StartEventRouter registers subscriptions and starts the event router
// using state.runCtx (independent of the external context).
//
// Key invariant: evtRouter.Run uses state.runCtx, NOT the external ctx.
// External ctx cancellation triggers phase9 → phase10 which calls evtRouter.Close;
// that closes runCtx internally, causing Run to return.
// ref: uber-go/fx app.go:L545-567 (run vs stop ctx separation).
func (b *Bootstrap) phase6StartEventRouter(s *phaseState) error {
	sub := s.sub
	if sub == nil {
		return b.checkNoEventRegistrars(s.asm)
	}

	var mws []outbox.SubscriptionMiddleware
	if !b.disableObservabilityRestore {
		mws = append(mws, outbox.ObservabilityContextMiddleware())
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

	if err := s.registerHealthChecker(eventRouterCheckerName, evtRouter.Health); err != nil {
		return err
	}

	slog.Info("bootstrap: starting event router",
		slog.Int("handler_count", evtRouter.HandlerCount()))

	routerErrCh := make(chan error, 1)
	// evtRouter.Run uses runCtx — not the external ctx.
	// ref: uber-go/fx run vs stop ctx separation.
	go func() {
		routerErrCh <- evtRouter.Run(s.runCtx)
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

// phase7StartHTTPServer creates and starts the HTTP server, wiring httpErrCh
// and a teardown that calls srv.Shutdown.
func (b *Bootstrap) phase7StartHTTPServer(s *phaseState) error {
	srv := &http.Server{
		Addr:              b.httpAddr,
		Handler:           s.rtr,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln := b.listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", b.httpAddr)
		if err != nil {
			return fmt.Errorf("bootstrap: listen %s: %w", b.httpAddr, err)
		}
	}

	httpErrCh := make(chan error, 1)
	go func() {
		slog.Info("bootstrap: HTTP server starting", slog.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

	s.httpErrCh = httpErrCh
	s.addTeardown(func(c context.Context) error {
		slog.Info("bootstrap: draining HTTP server")
		return srv.Shutdown(c)
	})
	return nil
}

// phase8StartWorkers starts the WorkerGroup using state.runCtx (independent of
// external ctx). The workerCancel is only called inside the teardown closure so
// that worker.Stop is the trigger for cancellation during phase10.
//
// Key invariant: workerCtx derives from state.runCtx, NOT from the external ctx.
// ref: uber-go/fx run vs stop ctx separation.
func (b *Bootstrap) phase8StartWorkers(s *phaseState) {
	wg := worker.NewWorkerGroup()
	for _, w := range b.workers {
		wg.Add(w)
	}

	// workerCtx derives from runCtx so external ctx cancel does NOT immediately
	// stop workers. Workers stop only when phase10 calls their teardown.
	workerCtx, workerCancel := context.WithCancel(s.runCtx)

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
		return wg.Stop(c)
	})
}

// phase9AwaitShutdownSignal blocks until one of: external ctx cancel, HTTP error,
// worker error, or router error. It returns a shutdownSignal describing what fired.
// It does NOT cancel workerCtx or runCtx — that happens in phase10.
func (b *Bootstrap) phase9AwaitShutdownSignal(ctx context.Context, s *phaseState) shutdownSignal {
	slog.Info("bootstrap: application started successfully")
	select {
	case <-ctx.Done():
		slog.Info("bootstrap: context cancelled, shutting down")
		return shutdownSignal{reason: reasonCtxCancel}
	case err := <-s.httpErrCh:
		return shutdownSignal{reason: reasonHTTPError, err: err}
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

	outcome := "success"
	if shutCtx.Err() != nil {
		outcome = "timeout"
	}
	m.countOutcome(outcome)

	// Safety net: cancel runCtx after all teardowns complete so any goroutine
	// still holding runCtx eventually unblocks.
	s.runCancel()

	teardownErr := errors.Join(teardownErrs...)
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
	s.reloads.BeginShutdown()
	s.hh.SetShuttingDown()

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
func (b *Bootstrap) phase10LIFOTeardown(shutCtx context.Context, s *phaseState) []error {
	var errs []error
	for i := len(s.teardowns) - 1; i >= 0; i-- {
		if err := s.teardowns[i](shutCtx); err != nil {
			slog.Error("bootstrap: shutdown step failed", slog.Any("error", err))
			errs = append(errs, err)
		}
	}
	return errs
}
