package composition

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/validation"
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
//  1. Guard that shared was produced by [NewSharedDeps] (sealed-construction
//     marker check) and that runtimeOptsFn is non-nil — startup invariants.
//  2. For each module: nil-guard, call [CellModule.Provide], accumulate cells +
//     cellOpts (module opts + one bootstrap.WithManagedResource derived per
//     ModuleResult.Resources entry) + a provisional ManagedResource stack, with
//     LIFO Close(ctx) rollback on any failure; nil-cell guard.
//  3. Call runtimeOptsFn(cells) to get runtimeOpts.  If it errors, rollback
//     provisional resources and return.
//  4. allOpts := runtimeOpts ++ cellOpts.
//  5. Return &App{clk: shared.Clock, opts: allOpts}.
//
// Resource ownership (single source — PR #591 / #1420): a module lists every
// ManagedResource it opened in ModuleResult.Resources and does NOT call
// bootstrap.WithManagedResource itself. Build derives BOTH channels from that
// one slice:
//   - Steady-state lifecycle: Build appends one bootstrap.WithManagedResource(r)
//     per resource to cellOpts, so bootstrap.Run manages health/worker/LIFO-Close
//     for the resource during the normal run (phase10 shutdown closes it).
//   - Pre-Run rollback: Build also appends r to a provisional stack and, if any
//     later step fails before returning the App, calls Close(ctx) in reverse
//     order (LIFO) so resources opened so far are released even though
//     bootstrap.Run never starts.
//
// The two channels never double-close: the rollback path fires only on failure
// (bootstrap.Run does not run), and the WithManagedResource path fires only on
// success. Deriving both from the single Resources slice makes the former
// double-write divergence (resource in opts but not provisional, or vice versa)
// structurally impossible — guarded by WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01,
// which bans WithManagedResource calls inside cellmodules/.
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

func (b *Builder) Build(
	ctx context.Context,
	shared *SharedDeps,
	runtimeOptsFn RuntimeOptionsFunc,
) (*App, error) {
	if err := validateBuildInputs(shared, runtimeOptsFn); err != nil {
		return nil, err
	}

	var cells []cell.Cell
	var cellOpts []bootstrap.Option
	// provisional holds resources opened so far; closed in reverse order if
	// any subsequent step fails. INVARIANT: every entry is non-nil — the loop
	// below rejects nil/typed-nil resources via validateModuleResources before
	// appending, so Close() below can never panic on a nil interface.
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
		res, err := resolveModuleResult(ctx, m, shared)
		if err != nil {
			rollback()
			return nil, err
		}
		cells = append(cells, res.Cell)
		// Single source: derive BOTH the steady-state WithManagedResource
		// registration (via managedResourceOpts) AND the pre-Run rollback stack
		// (provisional) from res.Resources, so the two can never diverge (the
		// former double-write bug). Modules do not call WithManagedResource
		// themselves (WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01).
		cellOpts = append(cellOpts, res.Opts...)
		cellOpts = append(cellOpts, managedResourceOpts(res.Resources)...)
		provisional = append(provisional, res.Resources...)
	}

	runtimeOpts, err := runtimeOptsFn(cells)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("composition.Builder.Build: runtime options: %w", err)
	}

	allOpts := append(runtimeOpts, cellOpts...) //nolint:gocritic // intentional: runtime opts first, then cell opts
	return &App{clk: shared.Clock, opts: allOpts}, nil
}

// resolveModuleResult calls one module's Provide and validates the result before
// it enters either lifecycle channel: the module must be non-nil, must return a
// non-nil Cell, and must not return any nil/typed-nil ManagedResource. Extracted
// from [Builder.Build] so the per-module loop body stays within the
// cognitive-complexity budget (same rationale as [managedResourceOpts]); the
// single returned error lets Build run rollback + return once.
func resolveModuleResult(ctx context.Context, m CellModule, shared *SharedDeps) (ModuleResult, error) {
	if m == nil {
		return ModuleResult{}, fmt.Errorf("composition.Builder.Build: module list contains nil")
	}
	res, err := m.Provide(ctx, shared)
	if err != nil {
		return ModuleResult{}, fmt.Errorf("composition.Builder.Build: module %q Provide: %w", m.ID(), err)
	}
	if res.Cell == nil {
		return ModuleResult{}, fmt.Errorf("composition.Builder.Build: module %q returned nil Cell "+
			"(use explicit Optional semantics if cell is optional)", m.ID())
	}
	// Reject nil resources BEFORE they enter the provisional rollback stack, so
	// rollback's Close() can never panic on a nil interface. The steady-state path
	// (managedResourceOpts → bootstrap.WithManagedResource) fail-fasts a nil
	// resource only at phase0; doing it here keeps both lifecycle channels
	// symmetric and fails fast at Build time instead.
	if err := validateModuleResources(m.ID(), res.Resources); err != nil {
		return ModuleResult{}, err
	}
	return res, nil
}

// validateModuleResources rejects nil/typed-nil entries in a module's Resources
// slice. [Builder.Build] calls it on each module's result before the resources
// enter either lifecycle channel (steady-state opts + provisional rollback
// stack), guaranteeing the provisional stack holds only non-nil resources so its
// Close() can never panic. A nil resource is a module wiring bug; surfacing it as
// a Build-time error keeps the rollback path symmetric with the steady-state path
// (where bootstrap.WithManagedResource fail-fasts a nil resource at phase0).
//
// Extracted from Build so the per-module loop body stays within the
// cognitive-complexity budget.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go — Stop runs only hooks that
// were successfully appended; bad inputs surface before any component starts.
func validateModuleResources(moduleID string, resources []kernellifecycle.ManagedResource) error {
	for i, r := range resources {
		if validation.IsNilInterface(r) {
			return fmt.Errorf("composition.Builder.Build: module %q returned nil "+
				"ManagedResource at Resources[%d] (resources must be non-nil)", moduleID, i)
		}
	}
	return nil
}

// managedResourceOpts derives one bootstrap.WithManagedResource option per
// resource — the steady-state half of the single-source resource contract. The
// caller ([Builder.Build]) appends the same resources to its provisional
// rollback stack, so both lifecycle channels come from the one Resources slice
// and cannot diverge. Extracted from Build so the per-module loop body stays
// within the cognitive-complexity budget.
//
// Nil entries: ModuleResult.Resources MUST NOT contain nil (see its godoc).
// [validateModuleResources] rejects any nil/typed-nil resource at Build time
// before it reaches this function or the rollback stack, so both channels only
// ever see non-nil resources. bootstrap.WithManagedResource additionally
// fail-fasts a stray nil at phase0 as defense in depth.
func managedResourceOpts(resources []kernellifecycle.ManagedResource) []bootstrap.Option {
	opts := make([]bootstrap.Option, 0, len(resources))
	for _, r := range resources {
		opts = append(opts, bootstrap.WithManagedResource(r))
	}
	return opts
}
