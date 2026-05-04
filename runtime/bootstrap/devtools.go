package bootstrap

// devtools.go — phase5 helper for building the devtools catalog handler.
//
// phase5InitDevtoolsHandler builds the devtools.Handler when b.devtoolsMeta
// is non-nil. It converts the kernel/governance.Graph to catalog.CellDepGraph
// and passes the build-time generated pkgGraph (if any). The handler is stored
// on phaseState; phase5CollectRouteGroups appends RouteGroup if present.

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
	"github.com/ghbvf/gocell/runtime/http/devtools"
)

// phase5InitDevtoolsHandler builds the devtools catalog handler when
// b.devtoolsMeta != nil. Returns nil (no error) when meta is nil — endpoint
// silently absent. The handler is stored on phaseState; phase5CollectRouteGroups
// appends RouteGroup if present.
func (b *Bootstrap) phase5InitDevtoolsHandler(_ context.Context, s *phaseState) error {
	if b.devtoolsMeta == nil {
		return nil
	}
	cellGraph := buildCellDepGraph(b.devtoolsMeta, b.clock)
	s.devtoolsHandler = devtools.NewHandler(b.devtoolsMeta, cellGraph, b.devtoolsPkgGraph, b.devtoolsRoot, b.clock, b.devtoolsWireSummaries)
	return nil
}

// buildCellDepGraph runs governance.DependencyChecker.Graph() and converts to
// catalog.CellDepGraph via catalog.NewCellDepGraph. Validation errors (cycles
// etc.) are logged at Warn but do not block bootstrap — endpoint surfaces a
// "best-effort" graph. BuiltAt is stamped from clk so operators can detect
// stale graphs.
func buildCellDepGraph(pm *metadata.ProjectMeta, clk clock.Clock) *catalog.CellDepGraph {
	dc := governance.NewDependencyChecker(pm)
	g, errs := dc.Graph()
	if len(errs) > 0 {
		slog.Warn("devtools: cell dep graph has validation errors; graph may be incomplete",
			slog.Int("error_count", len(errs)))
	}
	return catalog.NewCellDepGraph(g, clk)
}
