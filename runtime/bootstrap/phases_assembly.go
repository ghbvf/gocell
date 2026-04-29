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
		slog.Warn("bootstrap: no tracer provided, HTTP tracing is disabled and consumer spans will be no-op",
			slog.String("hint", "use bootstrap.WithTracer(...) in composition root"))
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
	asm := b.assemblyCore
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
			}
		}()
		if err := cr.OnConfigReload(evt); err != nil {
			ok = false
			slog.Error("bootstrap: config reload callback failed",
				slog.String("cell", id),
				slog.String("error", err.Error()),
				slog.Int64("config_generation", evt.Generation))
		}
	}()
	return ok
}
