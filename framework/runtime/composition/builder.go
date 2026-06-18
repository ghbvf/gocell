package composition

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/transport"
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
	migrations      []MigrationRegistration
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
	// Defensive copy: a variadic parameter aliases the caller's backing array
	// when invoked in spread form (composition.New(ids...) — see
	// cmd/corebundle/run.go). Without the clone the caller could mutate the
	// sealed closed set after New but before Build. slices.Clone of a nil/empty
	// slice yields nil/empty, preserving the empty-assembly degenerate case.
	return &Builder{expectedCellIDs: slices.Clone(expectedCellIDs)}
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
//     1b. Enforce the M12a closed-set bijection (validateClosedSet): the composed
//     module IDs must equal the assembly's declared cell-id set, fail-fast
//     before any Provide opens resources.
//  2. For each module (resolveModuleResult): nil-guard, call [CellModule.Provide],
//     nil-cell guard, closed-set identity guard (c.ID() == m.ID()), nil-resource
//     guard; then accumulate cells + cellOpts (module opts + one
//     bootstrap.WithManagedResource derived per ModuleResult.Resources entry) +
//     a provisional ManagedResource stack, with LIFO Close(ctx) rollback on any
//     failure.
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
//
// This guard keys on the module's self-reported ID() so it can fail fast before
// any Provide opens resources. It does NOT see the cell each module constructs;
// the Build provide loop closes that gap with a post-Provide c.ID() == m.ID()
// identity guard (resolveModuleResult), so the runtime cell identity is bound to
// the declaration validated here.
func (b *Builder) validateClosedSet() error {
	expected := make(map[string]struct{}, len(b.expectedCellIDs))
	for _, id := range b.expectedCellIDs {
		expected[id] = struct{}{}
	}
	// Sorted display for deterministic, readable diagnostics regardless of set size.
	closedSet := strings.Join(slices.Sorted(slices.Values(b.expectedCellIDs)), ", ")
	const fixHint = "add it to assembly.yaml cells or run `gocell generate assembly`"
	provided := make(map[string]struct{}, len(b.modules))
	for _, m := range b.modules {
		if m == nil {
			continue
		}
		id := m.ID()
		if _, dup := provided[id]; dup {
			return fmt.Errorf("composition.Builder.Build: duplicate cell module %q; "+
				"each assembly cell must be provided by exactly one module; %s", id, fixHint)
		}
		provided[id] = struct{}{}
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("composition.Builder.Build: cell %q is not in the assembly "+
				"closed set [%s]; %s", id, closedSet, fixHint)
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
	if err := b.validateMigrations(); err != nil {
		return nil, err
	}

	// Mint the shared in-process CellTransport holder (Epic #1423 US4 #1963)
	// BEFORE module resolution so each module's Provide can inject it into its
	// cross-cell sync contract clients. Empty on construction; bootstrap phase5
	// binds the built internal-listener handler (WithInProcessTransport below).
	// Centralized here so every composition root shares one mint point.
	txMetrics, err := transport.NewMetrics(shared.MetricsProvider)
	if err != nil {
		return nil, fmt.Errorf("composition.Builder.Build: transport metrics: %w", err)
	}
	// Share the SAME *transport.Metrics with both the in-process holder and any
	// remote transport a module resolves via celltransport.Resolve (#1966 P1.3):
	// reuse, never re-register, the single counter. The metrics is bundled with the
	// (optional) tracer into the SINGLE-SOURCE CrossCellObs so a module cannot wire
	// metrics while forgetting the tracer (#2251 P1.3).
	shared.TransportObs = transport.NewCrossCellObs(txMetrics, shared.Tracer)
	shared.InProcessTransport = transport.NewInProcess(txMetrics)

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

	// Inject the framework-controlled options that callers cannot override:
	//   1. WithControlPlaneTopology: lets bootstrap phase0 validate the internal-
	//      listener service-token store against the sealed adapter Topology (#1410
	//      review F1). shared.Topology is sealed (caller cannot forge it).
	//   2. WithDeploymentTopology: lets phase0 seal+validate the codegen-derived
	//      deployment placement spec into a runtime DeploymentTopology (#1962).
	//      shared.DeploymentTopology is derived via SpecForRole(generatedTopologyGroups(), role).
	// Both are appended to cellOpts — applied AFTER runtimeOpts in allOpts below —
	// so a caller's runtimeOptsFn cannot override them.
	//   3. WithInProcessTransport: hands bootstrap the SAME holder minted above so
	//      phase5 binds the built internal-listener handler into it (US4 #1963).
	cellOpts = append(cellOpts,
		bootstrap.WithControlPlaneTopology(shared.Topology),
		bootstrap.WithDeploymentTopology(shared.DeploymentTopology),
		bootstrap.WithInProcessTransport(shared.InProcessTransport),
	)
	//   4. WithTracer: when a tracer is configured, thread the SINGLE source
	//      (shared.Tracer) into bootstrap so router/in-process/event-router tracing
	//      use the SAME tracer the remote transport got via shared.TransportObs
	//      (#2251 P1.3). Nil = no tracing (no option appended; seam-only default).
	//      IsNilInterface (not a bare != nil) so a typed-nil tracer value is also
	//      treated as "unset" — consistent with the kernel projection coordinator's
	//      tracer guard, and avoids handing bootstrap a non-nil interface wrapping a
	//      nil pointer that would panic on Start (#2251 review F3).
	if !validation.IsNilInterface(shared.Tracer) {
		cellOpts = append(cellOpts, bootstrap.WithTracer(shared.Tracer))
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
// non-nil Cell, the constructed cell's ID must equal the module's ID (M12a
// closed-set identity guard, #1093), and it must not return any nil/typed-nil
// ManagedResource. Extracted from [Builder.Build] so the per-module loop body
// stays within the cognitive-complexity budget (same rationale as
// [managedResourceOpts]); the single returned error lets Build run rollback +
// return once.
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
	// Closed-set identity guard (M12a #1093): validateClosedSet validated
	// m.ID() ∈ closed set pre-Provide, but the identity that reaches runtime
	// (metric `cell` label, healthz probe names) is the cell's own c.ID() —
	// sourced from cell metadata, independent of m.ID(). Bind them: require
	// res.Cell.ID() == m.ID() so an in-set module cannot construct an out-of-set
	// cell. Since m.ID() ∈ closed set was already proven, this transitively
	// guarantees res.Cell.ID() ∈ closed set. Mirrors K8s runtime.Scheme:
	// registration enumerates the real object identity, not a wrapper label.
	if res.Cell.ID() != m.ID() {
		return ModuleResult{}, fmt.Errorf("composition.Builder.Build: module %q provided a cell whose ID is %q; "+
			"a module's ID must equal the ID of the cell it constructs — the closed-set guard validates the "+
			"module ID pre-Provide, so a mismatch would let an out-of-set cell identity reach runtime "+
			"(metric labels, healthz probes)", m.ID(), res.Cell.ID())
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
