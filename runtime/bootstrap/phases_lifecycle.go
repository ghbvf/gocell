package bootstrap

// phases_lifecycle.go — cell lifecycle hook discovery and health checker registration
// (phase3b + health-checker helpers called from phase5).
//
// Covers:
//   - phase3b: LifecycleContributor auto-discovery
//   - registerAllHealthCheckers / registerCellHealthCheckers / registerOneCellHealthCheckers
//   - registerConfigDriftChecker
//
// ref: uber-go/fx lifecycle.go — lifecycle hook registration ordering and
// duplicate-Name detection at Append time (kernel/lifecycle mirrors this contract).
// ref: kernel/cell.HealthContributor — mirrored auto-discovery pattern for health checkers.

import (
	"context"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/config"
)

// phase3bDiscoverLifecycleContributor auto-registers lifecycle hooks from all
// cells implementing cell.LifecycleContributor. Mirrors registerCellHealthCheckers
// to keep the discovery pattern symmetric.
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
		if err := b.lifecycle.kernel.Append(Hook{
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
