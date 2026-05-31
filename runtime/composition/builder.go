package composition

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// RuntimeOptionsFunc lets the composition root supply the fully-assembled
// runtime bootstrap options (assembly, three listeners + auth, consumer base,
// health/metrics).  It receives the constructed cells so the caller can build
// the assembly from them.
//
// Listener and auth construction MUST live in the caller (cmd/ or examples/),
// never inside runtime/composition — this package is forbidden by AUTH-PLAN-04
// from constructing auth plans (auth.NewAuthJWT / auth.NewAuthServiceToken …).
//
// Typical implementation: build a bootstrap.Assembly from cells, call
// auth.NewAuthJWTFromAssembly(asm) to obtain the JWT auth plan, then return
// bootstrap.WithAssembly(asm), bootstrap.WithListener(...) and related options.
//
// Returns (nil, nil) if there are no runtime-specific options to add.
//
// See examples/corebundlestarter/run.go for a runnable RuntimeOptionsFunc.
type RuntimeOptionsFunc func(cells []cell.Cell) ([]bootstrap.Option, error)

// Builder assembles a GoCell application from an ordered set of [CellModule]s.
//
// ref: uber-go/fx fx.New(opts...) — single assembly entry point used by both
// production (main) and tests.
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go — Manager
// accumulates options via functional options.
type Builder struct {
	modules []CellModule
}

// New returns a new Builder with no modules.
func New() *Builder {
	return &Builder{}
}

// With appends modules to the builder.  Calls are accumulating: successive
// With calls append, they do not replace.  Analogous to fx.Provide chaining.
func (b *Builder) With(modules ...CellModule) *Builder {
	b.modules = append(b.modules, modules...)
	return b
}

// Build orchestrates the assembly of all registered [CellModule]s and returns
// a ready-to-run [App].
//
// Flow:
//  1. Nil-guard and [SharedDeps.Validate] — startup invariant check.
//  2. For each module: nil-guard, call [CellModule.Provide], accumulate cells +
//     cellOpts + provisional ManagedResources, with LIFO Close(ctx) rollback on
//     any failure; nil-cell guard.
//  3. Call runtimeOptsFn(cells) to get runtimeOpts.  If it errors, rollback
//     provisional resources and return.
//  4. allOpts := runtimeOpts ++ cellOpts.
//  5. Return &App{clk: shared.Clock, opts: allOpts}.
//
// Cleanup-on-failure: resources returned by each module's Provide are accumulated
// into a provisional stack.  If any subsequent step fails, Build calls Close(ctx)
// on all accumulated resources in reverse order (LIFO) before returning the error.
// This prevents resource leaks when the assembly cannot complete.
//
// Note: module order is significant when modules share fields via *SharedDeps.
// A module that writes a shared field during Provide (e.g. auditcore writing
// SharedDeps.BootstrapLedgerStore) must appear before any module that reads that
// field.  Consult each SharedDeps field godoc for ordering constraints.
//
// ref: uber-go/fx fx.New(opts...) — single assembly entry point used by both
// production (main) and tests (fxtest.New).
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go —
// Manager.Start(ctx) error.
func (b *Builder) Build(
	ctx context.Context,
	shared *SharedDeps,
	runtimeOptsFn RuntimeOptionsFunc,
) (*App, error) {
	if shared == nil {
		return nil, fmt.Errorf("composition.Builder.Build: shared deps must be non-nil")
	}
	if err := shared.Validate(); err != nil {
		return nil, fmt.Errorf("composition.Builder.Build: shared deps validation: %w", err)
	}

	var cells []cell.Cell
	var cellOpts []bootstrap.Option
	// provisional holds resources opened so far; closed in reverse order if
	// any subsequent step fails.
	var provisional []kernellifecycle.ManagedResource

	rollback := func() {
		for _, v := range slices.Backward(provisional) {
			if closeErr := v.Close(ctx); closeErr != nil {
				slog.Warn("composition: provisional rollback Close failed",
					slog.Any("error", closeErr))
			}
		}
	}

	for _, m := range b.modules {
		if m == nil {
			rollback()
			return nil, fmt.Errorf("composition.Builder.Build: module list contains nil")
		}
		c, mOpts, mRes, err := m.Provide(ctx, shared)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("composition.Builder.Build: module %q Provide: %w", m.ID(), err)
		}
		if c == nil {
			rollback()
			return nil, fmt.Errorf("composition.Builder.Build: module %q returned nil Cell "+
				"(use explicit Optional semantics if cell is optional)", m.ID())
		}
		cells = append(cells, c)
		cellOpts = append(cellOpts, mOpts...)
		provisional = append(provisional, mRes...)
	}

	runtimeOpts, err := runtimeOptsFn(cells)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("composition.Builder.Build: runtime options: %w", err)
	}

	allOpts := append(runtimeOpts, cellOpts...) //nolint:gocritic // intentional: runtime opts first, then cell opts
	return &App{clk: shared.Clock, opts: allOpts}, nil
}
