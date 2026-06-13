// Package syscore is the platform composition module for the syscore Cell
// (#1860). It implements [composition.CellModule].
//
// This is a composition-root-layer package: it may import corecells/. It must
// NOT be imported by corecells/, runtime/, or adapters/.
//
// syscore is stateless — it serves the aggregated cell-health contract by reading
// the runtime HealthView that bootstrap injects into request context — so Provide
// takes nothing from SharedDeps and opens no ManagedResource.
//
// ref: uber-go/fx fx.Module("syscore", ...) — self-contained module.
package syscore

import (
	"context"

	syscell "github.com/ghbvf/gocell/corecells/syscore"
	"github.com/ghbvf/gocell/runtime/composition"
)

type module struct{}

// Module returns a composition.CellModule that wires the syscore Cell.
func Module() composition.CellModule { return module{} }

// ID returns the stable identifier used in error messages and logs.
func (module) ID() string { return "syscore" }

// Provide constructs the stateless syscore cell. It consumes no SharedDeps and
// returns no resources: the cross-cell health data is a framework-provided read
// view injected at request time, not a module-owned dependency.
func (module) Provide(_ context.Context, _ *composition.SharedDeps) (composition.ModuleResult, error) {
	return composition.ModuleResult{Cell: syscell.New()}, nil
}
