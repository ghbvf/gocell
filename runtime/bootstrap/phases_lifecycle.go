package bootstrap

// phases_lifecycle.go — cell lifecycle hook discovery and health probe wiring
// (phase3b + drainProbes called from phase5).
//
// Covers:
//   - phase3b: LifecycleHooks drain from RegistrySnapshot
//   - drainProbes: registers framework-level probes onto b.healthAggregator
//
// ref: uber-go/fx lifecycle.go — lifecycle hook registration ordering and
// duplicate-Name detection at Append time (kernel/lifecycle mirrors this contract).
// ref: kernel/healthz.Aggregator — cells register probes via reg.Healthz() during
// Init (written directly to the shared aggregator); bootstrap registers framework-level
// probes (config_watcher, config_drift) here.

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

// drainProbes registers framework-level probes onto b.healthAggregator.
//
// Cell-level repo probes are registered by cells during Init through the
// cellgen-generated <cellpkg>.RegisterRepoReady helper, which calls
// reg.Healthz().Register internally. Bootstrap doesn't need to "drain" a
// snapshot field anymore — the aggregator is shared between Registry and
// Handler at construction time, so probes registered on one side are
// visible to the other.
//
// This function registers framework-owned probes that aren't cell-owned:
//  1. option-supplied checkers (from WithHealthChecker) — adapter pool probes
//     such as postgres_ready, redis_ready, rabbitmq_ready, vault_transit_ready
//  2. config_watcher probe (when a config watcher is active)
//  3. config_drift probe (when the config supports generation tracking)
func (b *Bootstrap) drainProbes(s *phaseState) error {
	// Register option-supplied checkers (adapter pool probes, etc.).
	for _, hc := range b.healthCheckers {
		if err := s.registerHealthChecker(hc.name, hc.fn, b.healthAggregator); err != nil {
			return err
		}
	}
	// Register config_watcher probe when a watcher is active.
	if s.cfgWatcher != nil {
		cfgHealth := s.cfgWatcher.Health // func() error — wrap to ctx-aware signature
		if err := s.registerHealthChecker(configWatcherCheckerName, func(_ context.Context) error {
			return cfgHealth()
		}, b.healthAggregator); err != nil {
			return err
		}
	}
	return b.registerConfigDriftProbe(s)
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
