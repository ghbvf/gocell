package composition

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// CellModule is the contract by which a Cell declares itself to [Builder.Build].
// Each Cell provides a single *_module.go file that implements this interface,
// self-managing all Cell-specific dependency wiring (KeyProvider, PoolResource,
// cellOpts, etc.).
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

	// Provide resolves Cell-specific dependencies from the shared context
	// and returns:
	//
	//   - cell: the constructed Cell. Must be non-nil on success.
	//   - opts: bootstrap.Options the Cell needs (e.g. WithManagedResource for
	//     its PoolResource).
	//   - provisional: ManagedResources opened during Provide that must be
	//     closed if a subsequent module's Provide fails before bootstrap.Run
	//     activates the lifecycle. The caller ([Builder.Build]) owns rollback:
	//     on any failure it calls Close(ctx) in reverse order on all accumulated
	//     provisional resources.
	//
	// Modules MUST include in provisional every external connection opened
	// during Provide (PG pool, vault client, …) so Build can release them when
	// the assembly cannot complete. The same resources must also appear in the
	// returned opts via bootstrap.WithManagedResource so that bootstrap.Run
	// manages their lifecycle on the happy path.
	//
	// Cross-module value handoff via the former ModuleExports type has been
	// removed (Wave-1 #1423). Cell modules are now fully self-contained; cross-cell
	// communication happens via events (contract-based), not via in-process
	// Go handle passing at composition time.
	//
	// MODULE-PROVIDE-NO-VALUE-HANDOFF-01 (tools/archtest/module_provide_signature_frozen_test.go)
	// enforces that this signature has exactly two inputs (context.Context, *SharedDeps)
	// and four outputs (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error)
	// with no cross-module value-handoff channel.
	Provide(ctx context.Context, shared *SharedDeps) (
		cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error)
}
