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

	// Provide resolves Cell-specific dependencies from the shared context and
	// returns:
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
	// Some modules pass cross-module state by writing to shared (e.g.
	// SharedDeps.BootstrapLedgerStore is written by the auditcore module so
	// that the accesscore module can read it).  When this pattern is used,
	// [Builder.With] order matters: the writing module must be registered
	// before the reading module.
	Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error)
}
