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
	modules         []CellModule
	expectedCellIDs []string
}

// New returns a new Builder whose composed cells must form exactly the
// assembly's declared cell-id closed set (expectedCellIDs — the assembly.yaml
// cells list, threaded in by the generated entrypoint). Build fail-fasts if any
// composed module's ID is not in the set, if a declared cell is not provided by
// any module, or on duplicate module IDs (see Build).
//
// This is the M12a build-time closed-set guard (#1093): once multi-module
// assembly composition exists (#1086), the assembly.yaml cell set is the
// authoritative enumeration of legal cell identities, and a module composing a
// cell outside it is a configuration bug — rejected outright, not degraded to a
// `_runtime` sentinel. Mirrors K8s runtime.Scheme: registration-time enumeration
// + hard rejection of out-of-set identities.
func New(expectedCellIDs ...string) *Builder {
	return &Builder{expectedCellIDs: expectedCellIDs}
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
//  1. Guard that shared was produced by [NewSharedDeps] (sealed-construction
//     marker check) and that runtimeOptsFn is non-nil — startup invariants.
//  2. For each module: nil-guard, call [CellModule.Provide], accumulate cells +
//     cellOpts + provisional ManagedResources, with LIFO Close(ctx) rollback on
//     any failure; nil-cell guard.
//  3. Call runtimeOptsFn(cells) to get runtimeOpts.  If it errors, rollback
//     provisional resources and return.
//  4. allOpts := runtimeOpts ++ cellOpts.
//  5. Return &App{clk: shared.Clock, opts: allOpts}.
//
// Resource ownership (two channels, distinct phases — see pg-cell-template
// Chapter 4):
//   - Steady-state lifecycle: a module registers a resource by returning
//     bootstrap.WithManagedResource(res) in its opts (2nd return value). Those
//     opts flow into allOpts, so bootstrap.Run manages health/worker/LIFO-Close
//     for the resource during the normal run (phase10 shutdown closes it).
//   - Pre-Run rollback: the module ALSO returns the same resource in its 3rd
//     return value ([]ManagedResource). Build accumulates these into a
//     provisional stack and, if any later step fails before returning the App,
//     calls Close(ctx) in reverse order (LIFO) so resources opened so far are
//     released even though bootstrap.Run never starts.
//
// The two channels never double-close: the rollback path fires only on failure
// (bootstrap.Run does not run), and the WithManagedResource path fires only on
// success. Build does not itself convert provisional resources into bootstrap
// options — steady-state registration is the module's responsibility via its
// opts, so a resource appears at most once in the bootstrap managed set.
//
// Cross-module value handoff (formerly via ModuleExports) has been removed.
// Cell modules are now fully self-contained; cross-cell communication happens
// via events, not in-process Go handles (Wave-1 #1423 / MODULE-PROVIDE-NO-VALUE-HANDOFF-01).
//
// ref: uber-go/fx fx.New(opts...) — single assembly entry point used by both
// production (main) and tests (fxtest.New).
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go —
// Manager.Start(ctx) error.
// validateBuildInputs enforces the Build preconditions: shared must have been
// produced by [NewSharedDeps] (sealed-construction marker), its dependency set
// must still satisfy validate() at Build time, and runtimeOptsFn must be non-nil
// (error-first public API).
//
// The marker check alone is insufficient: NewSharedDeps stamps valid=true and
// returns a *SharedDeps whose exported fields the caller can subsequently mutate
// to a broken state (e.g. set JWTVerifier=nil) without clearing the marker. So
// Build re-runs validate() against the current field values rather than trusting
// the construction-time snapshot — mirroring Kubernetes CompletedOptions.Validate(),
// which validates the aggregate immediately before startup, not only at parse time.
func validateBuildInputs(shared *SharedDeps, runtimeOptsFn RuntimeOptionsFunc) error {
	if shared == nil || !shared.valid {
		return fmt.Errorf("composition.Builder.Build: shared deps must be built via composition.NewSharedDeps")
	}
	if err := shared.validate(); err != nil {
		return fmt.Errorf("composition.Builder.Build: shared deps invalid at Build time "+
			"(mutated after NewSharedDeps?): %w", err)
	}
	if runtimeOptsFn == nil {
		return fmt.Errorf("composition.Builder.Build: runtimeOptsFn must be non-nil")
	}
	return nil
}

// validateClosedSet enforces the M12a build-time closed-set invariant: the set
// of composed cell IDs must equal the assembly's declared cell-id set
// (b.expectedCellIDs) — a bijection. It runs before any module Provide so a
// misconfiguration fails fast without opening resources.
//
//   - every module ID must be in the declared set (no out-of-set cell),
//   - every declared cell must be provided by some module (no missing cell),
//   - no two modules may share a cell ID (no duplicate).
//
// nil modules are deliberately skipped here: the Build provide loop owns the
// nil-module path (with provisional-resource rollback), so the closed-set guard
// must not pre-empt it.
func (b *Builder) validateClosedSet() error {
	expected := make(map[string]struct{}, len(b.expectedCellIDs))
	for _, id := range b.expectedCellIDs {
		expected[id] = struct{}{}
	}
	provided := make(map[string]struct{}, len(b.modules))
	for _, m := range b.modules {
		if m == nil {
			continue
		}
		id := m.ID()
		if _, dup := provided[id]; dup {
			return fmt.Errorf("composition.Builder.Build: duplicate cell module %q; "+
				"each assembly cell must be provided by exactly one module", id)
		}
		provided[id] = struct{}{}
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("composition.Builder.Build: cell %q is not in the assembly "+
				"closed set %v; add it to assembly.yaml cells or correct the cell ID",
				id, b.expectedCellIDs)
		}
	}
	for _, id := range b.expectedCellIDs {
		if _, ok := provided[id]; !ok {
			return fmt.Errorf("composition.Builder.Build: assembly declares cell %q but no "+
				"module provides it; run `gocell generate assembly`", id)
		}
	}
	return nil
}

func (b *Builder) Build(
	ctx context.Context,
	shared *SharedDeps,
	runtimeOptsFn RuntimeOptionsFunc,
) (*App, error) {
	if err := validateBuildInputs(shared, runtimeOptsFn); err != nil {
		return nil, err
	}
	if err := b.validateClosedSet(); err != nil {
		return nil, err
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
