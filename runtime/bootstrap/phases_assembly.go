package bootstrap

// phases_assembly.go — phase functions for config loading, assembly init,
// pubsub init, auth wiring, and config-watcher binding (phases 0–4).
//
// ref: uber-go/fx app.go — sequential startup with transactional rollback.
// ref: go-micro/v2 config — OnChange callback binding after watcher is created.

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

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
	if b.closerNil {
		return fmt.Errorf("bootstrap: managed closer must not be nil in WithManagedCloser")
	}
	if b.rateLimiterNil {
		return fmt.Errorf("bootstrap: rate limiter must not be nil in WithRateLimiter")
	}
	if err := b.validateAuthJWTFromAssemblyPlans(); err != nil {
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
	if err := b.validateAssemblyClockAlignment(); err != nil {
		return err
	}
	// Advisory check (non-blocking): warn when the declared K8s grace period
	// is smaller than the bootstrap shutdown budget plus a 10s safety margin.
	b.warnTerminationGracePeriodInsufficient()
	return nil
}

// terminationGraceSafetyMargin is the SIGTERM → process response slack that
// must remain on top of shutdownTimeout before kubelet escalates to SIGKILL.
// 10s is the empirical floor — see docs/ops/graceful-shutdown-k8s.md.
const terminationGraceSafetyMargin = 10 * time.Second

// warnTerminationGracePeriodInsufficient emits a slog.Warn when the operator
// declared (via WithTerminationGracePeriod) a K8s terminationGracePeriodSeconds
// that is smaller than the framework's own shutdownTimeout +
// terminationGraceSafetyMargin.
//
// preShutdownDelay does NOT appear in the formula because phase10's shutCtx
// (phases_shutdown.go) bounds the entire four-stage shutdown — readiness
// flip + preShutdownDelay + HTTP drain + LIFO teardown — by shutdownTimeout.
// preShutdownDelay is consumed inside shutdownTimeout, not on top of it
// (see WithPreShutdownDelay godoc and roadmap N3 deviation note).
//
// Behavior is advisory: the helper never returns or panics; misalignment is a
// deployment-config defect that we surface but do not block on. A zero
// terminationGracePeriod skips the check (the option was not applied);
// shutdownTimeout=0 also skips because it indicates a half-built Bootstrap
// (production code paths via New() always populate shutdownTimeout).
//
// ref: docs/ops/graceful-shutdown-k8s.md — formula and pod-spec example.
func (b *Bootstrap) warnTerminationGracePeriodInsufficient() {
	if b.terminationGracePeriod <= 0 || b.shutdownTimeout <= 0 {
		return
	}
	minRequired := b.shutdownTimeout + terminationGraceSafetyMargin
	if b.terminationGracePeriod >= minRequired {
		return
	}
	const hint = "increase Kubernetes pod terminationGracePeriodSeconds to " +
		">= shutdownTimeout + 10s, or reduce the bootstrap shutdown budget"
	slog.Warn("bootstrap: terminationGracePeriodSeconds insufficient for graceful shutdown",
		slog.Duration("termination_grace_period", b.terminationGracePeriod),
		slog.Duration("shutdown_timeout", b.shutdownTimeout),
		slog.Duration("pre_shutdown_delay", b.preShutdownDelay),
		slog.Duration("minimum_required", minRequired),
		slog.String("hint", hint))
}

// validateAssemblyClockAlignment ensures that when a pre-built assembly is
// supplied via WithAssembly, its internal clock matches the bootstrap clock
// set via WithClock. A mismatch means lifecycle / shutdown timers and cell
// Dependencies.Clock would disagree on the current time — a subtle source of
// flakiness in tests and incorrect timeout behavior in production.
//
// Callers must pass the same clock.Clock instance to both:
//
//	bootstrap.WithClock(clk) and assembly.New(assembly.Config{Clock: clk})
func (b *Bootstrap) validateAssemblyClockAlignment() error {
	if b.assemblyCore == nil {
		return nil
	}
	if b.assemblyCore.Clock() != b.clock {
		return fmt.Errorf(
			"bootstrap: clock mismatch — the assembly's Clock and the bootstrap's Clock are different instances; " +
				"pass the same clock.Clock instance to both bootstrap.WithClock and assembly.New(Config{Clock: ...})")
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
		w, err := b.configWatcherFactory(b.configPath, b.clock)
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
	// (phase7, HTTP side) and NewContractTracingSubscriber (phase6, consumer
	// side) at the construction call sites. When WithTracer was not supplied,
	// HTTP tracing is disabled and wrapper.WrapSubscriber falls back to
	// NoopTracer so spans degrade silently — no package-level setup needed.
	if b.wrapperTracer == nil {
		slog.Warn("bootstrap: no tracer provided, HTTP tracing is disabled and consumer spans will be no-op",
			slog.String("hint", "use bootstrap.WithTracer(...) in composition root"))
	}
	return nil
}

// phase2InitPubSub initializes the publisher and subscriber.
// When neither is provided a default InMemoryEventBus serves both roles.
func (b *Bootstrap) phase2InitPubSub(s *phaseState) {
	pub := b.publisher
	sub := b.subscriber
	if pub == nil && sub == nil {
		eb := eventbus.New(eventbus.WithClock(b.clock))
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
	asm := b.assemblyCore
	if asm == nil {
		cfg := assembly.Config{ID: "default", DurabilityMode: cell.DurabilityDemo, Clock: b.clock}
		if b.metricsProvider != nil {
			cfg.MetricsProvider = b.metricsProvider
		}
		asm = assembly.New(cfg)
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
	// Copy the per-cell RegistrySnapshots from the assembly so that later phases
	// (3b / 5 / 6) drain them instead of type-asserting on the live cell instances.
	s.cellSnapshots = asm.Snapshots()

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
		// Use context.Background as the root: the watcher fires from a goroutine
		// that has no caller context. Timeout is applied per-callback inside
		// invokeReloader using s.asm.ReloadTimeout().
		b.applyConfigReload(context.Background(), s, evt, yamlPath, envPrefix)
	}
}

// applyConfigReload performs the actual config reload: Reload → Diff → notify cells.
// Extracted to keep buildOnChangeCallback's cognitive complexity ≤ 15.
func (b *Bootstrap) applyConfigReload(ctx context.Context, s *phaseState, evt config.WatchEvent, yamlPath, envPrefix string) {
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
	allOK := b.notifyCellsConfigChanged(ctx, s, newSnap, added, updated, removed, gen)
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

// notifyCellsConfigChanged notifies all cells that registered OnConfigReload
// callbacks (via cell.Registry.OnConfigReload) about a config change.
// Returns true only when every callback applied the change successfully.
func (b *Bootstrap) notifyCellsConfigChanged(
	ctx context.Context,
	s *phaseState,
	newSnap map[string]any,
	added, updated, removed []string,
	gen int64,
) bool {
	timeout := s.asm.ReloadTimeout()
	allOK := true
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, req := range snap.ConfigReloaders {
			if !shouldNotifyReloader(req, added, updated, removed) {
				continue
			}
			cfgSnap := buildConfigSnapForReloader(req, newSnap)
			evt := cell.ConfigChangeEvent{
				Added:      cloneStrings(added),
				Updated:    cloneStrings(updated),
				Removed:    cloneStrings(removed),
				Config:     cfgSnap,
				Generation: gen,
			}
			if !invokeReloader(ctx, id, req.Fn, evt, timeout) {
				allOK = false
			}
		}
	}
	return allOK
}

// shouldNotifyReloader returns true when the reload request has no prefix
// filter or at least one changed key matches a declared prefix.
func shouldNotifyReloader(req cell.ConfigReloadRequest, added, updated, removed []string) bool {
	if len(req.Prefixes) == 0 {
		return true
	}
	changedKeys := make([]string, 0, len(added)+len(updated)+len(removed))
	changedKeys = append(changedKeys, added...)
	changedKeys = append(changedKeys, updated...)
	changedKeys = append(changedKeys, removed...)
	return config.NewKeyFilter(req.Prefixes...).Matches(changedKeys)
}

// buildConfigSnapForReloader constructs the config snapshot for a single reload
// request, optionally filtered to the declared key prefixes.
func buildConfigSnapForReloader(req cell.ConfigReloadRequest, newSnap map[string]any) map[string]any {
	snap := cloneMap(newSnap)
	if len(req.Prefixes) > 0 {
		snap = filterMapByPrefixes(snap, req.Prefixes)
	}
	return snap
}

// invokeReloader calls fn inside a recover fence with an optional per-call
// timeout derived from the assembly's ReloadTimeout. Returns true on success.
//
// timeout semantics:
//   - positive: a child context with that deadline is derived from ctx and
//     passed to fn; cancel is deferred after fn returns.
//   - zero or negative: ctx is passed directly (no additional deadline).
//
// ref: etcd clientv3 Watch ctx propagation — watchers propagate the caller's ctx.
// ref: k8s SharedInformer — informer callbacks carry a bounded ctx.
func invokeReloader(
	ctx context.Context,
	id string,
	fn func(context.Context, cell.ConfigChangeEvent) error,
	evt cell.ConfigChangeEvent,
	timeout time.Duration,
) (ok bool) {
	ok = true
	func() {
		defer func() {
			if r := recover(); r != nil {
				ok = false
				slog.Error("bootstrap: config reload callback panic",
					slog.String("cell", id),
					slog.String("type", fmt.Sprintf("%T", r)))
			}
		}()

		callCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		if err := fn(callCtx, evt); err != nil {
			ok = false
			slog.Error("bootstrap: config reload callback failed",
				slog.String("cell", id),
				slog.String("error", err.Error()),
				slog.Int64("config_generation", evt.Generation))
		}
	}()
	return ok
}
