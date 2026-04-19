package bootstrap

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
)

// BuildApp is the canonical assembly entry point shared by main and integration
// tests. It validates shared dependencies, then delegates per-Cell wiring to
// each CellModule.
//
// Flow:
//  1. shared.Validate() — startup invariant check (all required deps present).
//  2. For each module: module.Provide(ctx, shared) → cell.Cell + []Option.
//  3. Return aggregated (cells, opts). The cmd layer calls buildAssembly(cells...)
//     + bootstrap.New(opts...) to complete the wiring.
//
// BuildApp returns ([]cell.Cell, []Option, error) rather than *Bootstrap because
// assembly.NewCoreAssembly requires concrete Cell types that runtime/bootstrap
// cannot generalise. The cmd layer bridges that gap after BuildApp returns.
//
// ref: uber-go/fx fx.New(opts...) — single assembly entry point used by both
// production (main) and tests (fxtest.New).
//
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
func BuildApp(
	ctx context.Context,
	shared SharedDepsProvider,
	modules ...CellModule,
) ([]cell.Cell, []Option, error) {
	if shared == nil {
		return nil, nil, fmt.Errorf("bootstrap: BuildApp requires non-nil shared deps")
	}
	if err := shared.Validate(); err != nil {
		return nil, nil, fmt.Errorf("bootstrap: shared deps validation: %w", err)
	}

	var cells []cell.Cell
	var opts []Option

	for _, m := range modules {
		if m == nil {
			return nil, nil, fmt.Errorf("bootstrap: module list contains nil")
		}
		c, mOpts, err := m.Provide(ctx, shared)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: module %q Provide: %w", m.ID(), err)
		}
		if c != nil {
			cells = append(cells, c)
		}
		opts = append(opts, mOpts...)
	}

	return cells, opts, nil
}
