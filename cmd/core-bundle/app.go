package main

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// BuildApp is the canonical assembly entry point shared by main and integration
// tests. It validates shared dependencies, then delegates per-Cell wiring to
// each CellModule.
//
// Flow:
//  1. shared.Validate() — startup invariant check (all required deps present).
//  2. For each module: module.Provide(ctx, shared) → cell.Cell + []bootstrap.Option.
//  3. Return aggregated (cells, opts). The cmd layer calls buildAssembly(cells...)
//     + bootstrap.New(opts...) to complete the wiring.
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

	for _, m := range modules {
		if m == nil {
			return nil, nil, fmt.Errorf("BuildApp: module list contains nil")
		}
		c, mOpts, err := m.Provide(ctx, shared)
		if err != nil {
			return nil, nil, fmt.Errorf("BuildApp: module %q Provide: %w", m.ID(), err)
		}
		if c == nil {
			return nil, nil, fmt.Errorf("BuildApp: module %q returned nil Cell (use explicit Optional semantics if cell is optional)", m.ID())
		}
		cells = append(cells, c)
		opts = append(opts, mOpts...)
	}

	return cells, opts, nil
}
