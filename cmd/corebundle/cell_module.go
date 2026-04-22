package main

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// CellModule is the contract by which a Cell declares itself to BuildApp.
// Each Cell has a single *_module.go file that implements this interface,
// self-managing all Cell-specific dependency wiring (KeyProvider, PGResource,
// cellOpts, etc.).
//
// CellModule is defined here (cmd/corebundle) rather than in runtime/bootstrap
// because Provide must accept the concrete *SharedDeps type for type safety.
// Moving it here avoids a circular dependency (runtime/bootstrap cannot import
// cmd/corebundle).
//
// ref: uber-go/fx fx.Module(name, opts...) — each module is self-contained
// and registers its own providers.
// ref: Go proverbs "accept interfaces, return structs" — single concrete impl
// means no interface; future second impl introduces the interface at that point.
//
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type CellModule interface {
	// ID returns a stable identifier used in error messages.
	ID() string
	// Provide resolves Cell-specific dependencies from the shared context and
	// returns the constructed cell.Cell plus any bootstrap.Options it requires
	// (e.g. WithManagedResource for a PGResource).
	Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error)
}
