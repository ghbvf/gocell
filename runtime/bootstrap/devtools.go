package bootstrap

// devtools.go — phase5 helper for building the devtools catalog handler.
//
// phase5InitDevtoolsHandler builds the devtools.Handler when b.devtoolsMeta
// is non-nil. It converts the kernel/governance.Graph to kernel/metadata.CellDepGraph
// and passes the build-time generated pkgGraph (if any). The handler is stored
// on phaseState; phase5CollectRouteGroups appends RouteGroup if present.

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
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
	s.devtoolsHandler = devtools.NewHandler(b.devtoolsMeta, cellGraph, b.devtoolsPkgGraph, b.devtoolsRoot, b.clock)
	return nil
}

// buildCellDepGraph runs governance.DependencyChecker.Graph() and converts to
// kernel/metadata.CellDepGraph. Validation errors (cycles etc.) are logged at
// Warn but do not block bootstrap — endpoint surfaces "best-effort" graph.
// BuiltAt is stamped from clk so operators can see how stale the graph is.
func buildCellDepGraph(pm *metadata.ProjectMeta, clk clock.Clock) *metadata.CellDepGraph {
	dc := governance.NewDependencyChecker(pm)
	g, errs := dc.Graph()
	if len(errs) > 0 {
		slog.Warn("devtools: cell dep graph has validation errors; graph may be incomplete",
			slog.Int("error_count", len(errs)))
	}
	out := &metadata.CellDepGraph{
		Nodes:   append([]string(nil), g.Nodes...),
		Edges:   make([]metadata.CellEdge, 0, len(g.Edges)),
		BuiltAt: clk.Now().UTC().Format(time.RFC3339),
	}
	sort.Strings(out.Nodes)
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, metadata.CellEdge{From: e.From, To: e.To})
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		return out.Edges[i].To < out.Edges[j].To
	})
	return out
}
