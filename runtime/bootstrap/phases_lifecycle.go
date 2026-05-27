package bootstrap

// phases_lifecycle.go — cell lifecycle hook discovery and health probe wiring
// (phase3b + drainProbes called from phase5).
//
// Covers:
//   - phase3b: LifecycleHooks drain from RegistrySnapshot
//   - drainProbes: drains cell probes from RegistrySnapshot.Probes + registers
//     framework-level probes, all onto b.healthAggregator
//
// ref: uber-go/fx lifecycle.go — lifecycle hook registration ordering and
// duplicate-Name detection at Append time (kernel/lifecycle mirrors this contract).
// ref: kernel/healthz.Aggregator — cells accumulate probes via reg.RegisterReadiness
// during Init into RegistrySnapshot.Probes; bootstrap drains them onto the
// runtime aggregator here, alongside framework probes (config_watcher,
// config_drift). The recorder holds no live aggregator.

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/runtime/config"
)

// phase3bDrainLifecycleHooks drains LifecycleHooks from each cell's
// RegistrySnapshot and registers them with the bootstrap Lifecycle. Hooks are
// appended in cell-registration order; within a cell they are appended in
// declaration order.
//
// Must run after phase3InitAssembly (s.cellSnapshots is populated there) and
// before lifecycle.Start(ctx).
//
// Cross-path uniqueness: Lifecycle.Append is the single source of truth for
// duplicate-Name detection (returns ErrDuplicateHookName). That guard covers
// every entry path into the shared Lifecycle — phase3b snapshot drain,
// WithLifecycle explicit registration, and any future callers — without
// needing a phase-local "seen" map that could drift from reality.
//
// ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go — Hook, Append ordering.
func (b *Bootstrap) phase3bDrainLifecycleHooks(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, h := range snap.LifecycleHooks {
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
	}
	return nil
}

// drainProbes registers all readiness probes onto b.healthAggregator. It runs
// in phase 5 — after every cell's Init has run and RegistrySnapshots are
// populated. Two probe sources:
//
//  1. Cell-level probes — accumulated into each cell's RegistrySnapshot.Probes
//     when the typed funnels — the cellgen <cellpkg>.RegisterReadiness helper
//     and the shared kernel cell.RegisterEmitterHealthProbes — call
//     reg.RegisterReadiness(...) during Init. The recorder is a pure
//     accumulator (it holds no live aggregator);
//     bootstrap owns the only runtime aggregator and drains the snapshot onto
//     it here — exactly mirroring how RouteGroups / Subscriptions /
//     LifecycleHooks are drained from the snapshot.
//  2. Framework-owned probes that aren't cell-owned:
//     a. option-supplied checkers (from WithHealthChecker) — adapter pool
//     probes such as postgres_ready, redis_ready, rabbitmq_ready
//     b. config_watcher probe (when a config watcher is active)
//     c. config_drift probe (when the config supports generation tracking)
func (b *Bootstrap) drainProbes(s *phaseState) error {
	// 1. Cell-level probes from each cell's RegistrySnapshot.
	if err := b.drainCellProbes(s); err != nil {
		return err
	}
	// 2a. Register option-supplied checkers (adapter pool probes, etc.).
	for _, hc := range b.healthCheckers {
		if err := s.registerHealthChecker(hc.name, hc.fn, b.healthAggregator); err != nil {
			return err
		}
	}
	// 2b. Register config_watcher probe when a watcher is active.
	if s.cfgWatcher != nil {
		cfgHealth := s.cfgWatcher.Health // func() error — wrap to ctx-aware signature
		if err := s.registerHealthChecker(configWatcherCheckerName, func(_ context.Context) error {
			return cfgHealth()
		}, b.healthAggregator); err != nil {
			return err
		}
	}
	// 2c. config_drift probe.
	return b.registerConfigDriftProbe(s)
}

// drainCellProbes registers each cell's RegistrySnapshot.Probes onto
// b.healthAggregator in cell-registration order. Probes were accumulated by
// the cellgen RegisterReadiness helper and the kernel
// cell.RegisterEmitterHealthProbes funnel during Init.
// Duplicate probe names (within or across cells) fail-fast via the
// aggregator's first-wins healthz.ErrDuplicateProbe.
func (b *Bootstrap) drainCellProbes(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, p := range snap.Probes {
			if err := b.healthAggregator.Register(p); err != nil {
				return fmt.Errorf("bootstrap: cell %q probe %q: %w", id, p.Name(), err)
			}
		}
	}
	return nil
}

// registerConfigDriftProbe registers the config_drift health probe when the
// config supports generation tracking.
func (b *Bootstrap) registerConfigDriftProbe(s *phaseState) error {
	cfg := s.cfg
	g, gOK := cfg.(config.Generationer)
	og, ogOK := cfg.(config.ObservedGenerationer)
	if !gOK || !ogOK {
		return nil
	}
	probe := healthz.NewProbe(configDriftCheckerName, func(_ context.Context) error {
		if config.HasDrift(cfg) {
			return fmt.Errorf("config drift: generation %d, observed %d",
				g.Generation(), og.ObservedGeneration())
		}
		return nil
	})
	if err := b.healthAggregator.Register(probe); err != nil {
		return fmt.Errorf("bootstrap: register probe %q: %w", configDriftCheckerName, err)
	}
	s.registeredCheckers[configDriftCheckerName] = struct{}{}
	return nil
}
