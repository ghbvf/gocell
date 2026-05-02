package bootstrap

// phases_lifecycle.go — cell lifecycle hook discovery and health checker registration
// (phase3b + health-checker helpers called from phase5).
//
// Covers:
//   - phase3b: LifecycleHooks drain from RegistrySnapshot
//   - registerAllHealthCheckers / registerCellHealthCheckers / registerOneCellHealthCheckers
//   - registerConfigDriftChecker
//
// ref: uber-go/fx lifecycle.go — lifecycle hook registration ordering and
// duplicate-Name detection at Append time (kernel/lifecycle mirrors this contract).
// ref: kernel/cell.Registry.Health — cells register probes via reg.Health during Init;
// bootstrap drains HealthCheckers from RegistrySnapshot in this phase.

import (
	"context"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/runtime/config"
)

// phase3bDiscoverLifecycleContributor drains LifecycleHooks from each cell's
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
func (b *Bootstrap) phase3bDiscoverLifecycleContributor(s *phaseState) error {
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

// registerCellHealthCheckers drains HealthCheckers from each cell's RegistrySnapshot.
// Checkers are registered in sorted order (by name) for deterministic readyz output.
func (b *Bootstrap) registerCellHealthCheckers(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		if err := b.registerOneCellHealthCheckers(s, id, snap.HealthCheckers); err != nil {
			return err
		}
	}
	return nil
}

// registerOneCellHealthCheckers registers all health checkers from a single
// cell's snapshot map, in sorted order.
func (b *Bootstrap) registerOneCellHealthCheckers(s *phaseState, id string, cellCheckers map[string]func(context.Context) error) error {
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
	return s.registerHealthChecker(configDriftCheckerName, func(_ context.Context) error {
		if config.HasDrift(cfg) {
			return fmt.Errorf("config drift: generation %d, observed %d",
				g.Generation(), og.ObservedGeneration())
		}
		return nil
	})
}
