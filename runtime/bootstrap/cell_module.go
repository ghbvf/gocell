package bootstrap

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// SharedDepsProvider is the BuildApp-facing contract for cross-cutting
// dependencies — every Cell may need them but they are not Cell-specific.
// The concrete implementation (e.g. *SharedDeps) lives in the cmd layer and
// carries Topology, JWT, Prom, EventBus, etc. Validate() is the startup
// invariant check entry point.
//
// ref: uber-go/fx — public package defines the contract; implementation lives
// in the consumer (cmd/ layer).
//
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type SharedDepsProvider interface {
	Validate() error
}

// CellModule is the contract by which a Cell declares itself to BuildApp.
// Each Cell has a single *_module.go file that implements this interface,
// self-managing all Cell-specific dependency wiring (KeyProvider, PGResource,
// cellOpts, etc.).
//
// ref: uber-go/fx fx.Module(name, opts...) — each module is self-contained
// and registers its own providers.
//
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type CellModule interface {
	// ID returns a stable identifier used in error messages.
	ID() string
	// Provide resolves Cell-specific dependencies from the shared context and
	// returns the constructed cell.Cell plus any bootstrap.Options it requires
	// (e.g. WithManagedResource for a PGResource).
	Provide(ctx context.Context, shared SharedDepsProvider) (cell.Cell, []Option, error)
}
