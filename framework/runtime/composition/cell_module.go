package composition

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// ModuleResult is the single-source result of [CellModule.Provide].
//
// It carries the constructed Cell plus the two kinds of bootstrap contribution a
// module can make:
//
//   - Opts: non-resource bootstrap options (e.g. bootstrap.WithRelay). These
//     flow into the assembly verbatim.
//   - Resources: the ManagedResources the module opened during Provide (PG pool,
//     vault client, rate-limiter cleanup goroutine, …). This is the SINGLE source
//     for resource lifecycle: [Builder.Build] derives BOTH the steady-state
//     registration (one bootstrap.WithManagedResource(r) per resource, so
//     bootstrap.Run owns its Probes()/Worker()/LIFO-Close on the happy path) AND
//     the pre-Run rollback stack (Close(ctx) in reverse order if a later module
//     fails before bootstrap.Run starts). Because both are derived from the one
//     slice, they can never diverge.
//
// Modules MUST NOT call bootstrap.WithManagedResource themselves — that is
// forbidden inside cellmodules/ by WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01. List
// the resource in Resources and the Builder funnels it. Before #1420 a module had
// to write the same resource into both an opt and a provisional return; forgetting
// either half leaked the resource or dropped its /readyz probe. The single
// Resources field closes that double-write.
//
// No cross-module value-handoff field is permitted (no Exports): the former
// ModuleExports channel was removed in Wave-1 #1423 and MUST stay removed. The
// field set is frozen by MODULE-PROVIDE-NO-VALUE-HANDOFF-01.
type ModuleResult struct {
	// Cell is the constructed Cell. Must be non-nil on success.
	Cell cell.Cell

	// Opts are non-resource bootstrap options the Cell needs (e.g.
	// bootstrap.WithRelay). Resources MUST NOT be registered here via
	// bootstrap.WithManagedResource — use the Resources field; the Builder
	// derives the WithManagedResource registration from it.
	Opts []bootstrap.Option

	// Resources are the ManagedResources opened during Provide. The Builder
	// derives both steady-state registration and pre-Run rollback from this one
	// slice (single source). Entries MUST be non-nil.
	Resources []kernellifecycle.ManagedResource
}

// CellModule is the contract by which a Cell declares itself to [Builder.Build].
// Each Cell provides a single *_module.go file that implements this interface,
// self-managing all Cell-specific dependency wiring (KeyProvider, PoolResource,
// cellOpts, etc.).
//
// # Cross-cell communication
//
// Cells communicate with each other exclusively via contracts: HTTP contracts
// (request/response) and event contracts (publish/subscribe). In-process Go
// handle passing between Cells at composition time is prohibited — the former
// ModuleExports type has been removed (Wave-1 #1423). If a Cell needs to react
// to another Cell's state change, it subscribes to an event contract; it does
// not receive a direct reference to the other Cell's internal store or service.
//
// See MODULE-PROVIDE-NO-VALUE-HANDOFF-01 archtest for the machine enforcement.
//
// ref: uber-go/fx fx.Module(name, opts...) — each module is self-contained and
// registers its own providers.
// ref: Go proverbs "accept interfaces, return structs" — when there is only one
// concrete implementation, the interface is introduced at the point where a
// second impl appears; CellModule has multiple impls (one per Cell) so the
// interface is appropriate here.
type CellModule interface {
	// ID returns a stable identifier used in error messages and logs.
	ID() string

	// Provide resolves Cell-specific dependencies from the shared context and
	// returns a [ModuleResult] (the constructed Cell + non-resource opts +
	// the single-source ManagedResource list) or an error.
	//
	// Resource lifecycle is single-source: the module lists every external
	// connection it opened (PG pool, vault client, …) in ModuleResult.Resources,
	// and [Builder.Build] derives BOTH the happy-path bootstrap.WithManagedResource
	// registration AND the pre-Run rollback from that one slice. The module MUST
	// NOT call bootstrap.WithManagedResource itself.
	//
	// Cross-module value handoff via the former ModuleExports type has been
	// removed (Wave-1 #1423). Cell modules are now fully self-contained; cross-cell
	// communication happens via events (contract-based), not via in-process
	// Go handle passing at composition time.
	//
	// MODULE-PROVIDE-NO-VALUE-HANDOFF-01 (tools/archtest/module_provide_signature_frozen_test.go)
	// enforces that this signature has exactly two inputs (context.Context, *SharedDeps)
	// and two outputs (ModuleResult, error), and that ModuleResult has exactly the
	// fields {Cell, Opts, Resources} with no cross-module value-handoff channel.
	Provide(ctx context.Context, shared *SharedDeps) (ModuleResult, error)
}
