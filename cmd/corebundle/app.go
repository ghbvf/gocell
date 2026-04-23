package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// BuildApp is the canonical assembly entry point shared by main and integration
// tests. It validates shared dependencies, then delegates per-Cell wiring to
// each CellModule.
//
// Flow:
//  1. shared.Validate() — startup invariant check (all required deps present).
//  2. For each module: module.Provide(ctx, shared) → cell.Cell + []bootstrap.Option
//     + []lifecycle.ManagedResource.
//  3. Return aggregated (cells, opts). The cmd layer calls buildAssembly(...)
//     + bootstrap.New(opts...) to complete the wiring.
//
// Cleanup-on-failure: resources returned by each module's Provide are
// accumulated into a provisional stack. If any subsequent Provide fails,
// BuildApp calls Close(ctx) on all previously accumulated resources in
// reverse order before returning the error. This prevents resource leaks
// when the assembly cannot complete (e.g. a PG pool opened by module A
// before module B's Provide fails).
//
// BuildApp returns ([]cell.Cell, []bootstrap.Option, error) rather than
// *bootstrap.Bootstrap because assembly.NewCoreAssembly requires concrete Cell
// types. The caller bridges that gap after BuildApp returns.
//
// ref: uber-go/fx fx.New(opts...) — single assembly entry point used by both
// production (main) and tests (fxtest.New).
//
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
func BuildApp(
	ctx context.Context,
	shared *SharedDeps,
	modules ...CellModule,
) ([]cell.Cell, []bootstrap.Option, error) {
	if shared == nil {
		return nil, nil, fmt.Errorf("BuildApp: requires non-nil shared deps")
	}
	if err := shared.Validate(); err != nil {
		return nil, nil, fmt.Errorf("BuildApp: shared deps validation: %w", err)
	}

	var cells []cell.Cell
	var opts []bootstrap.Option
	// provisional holds resources opened so far; rolled back in reverse order
	// if any subsequent Provide (or nil-cell check) fails.
	var provisional []kernellifecycle.ManagedResource

	rollback := func() {
		// Stop in LIFO order so dependents are closed before dependencies.
		for i := len(provisional) - 1; i >= 0; i-- {
			if stopErr := provisional[i].Close(ctx); stopErr != nil {
				// Best-effort: log and continue; the process is aborting.
				// Discarding the error here is intentional — we are already
				// handling a primary error and cannot propagate rollback errors
				// to the caller without losing the root cause.
				slog.Warn("BuildApp: provisional rollback Close failed",
					slog.String("error", stopErr.Error()))
			}
		}
	}

	for _, m := range modules {
		if m == nil {
			rollback()
			return nil, nil, fmt.Errorf("BuildApp: module list contains nil")
		}
		c, mOpts, mRes, err := m.Provide(ctx, shared)
		if err != nil {
			rollback()
			return nil, nil, fmt.Errorf("BuildApp: module %q Provide: %w", m.ID(), err)
		}
		if c == nil {
			rollback()
			return nil, nil, fmt.Errorf("BuildApp: module %q returned nil Cell (use explicit Optional semantics if cell is optional)", m.ID())
		}
		cells = append(cells, c)
		opts = append(opts, mOpts...)
		provisional = append(provisional, mRes...)
	}

	return cells, opts, nil
}
