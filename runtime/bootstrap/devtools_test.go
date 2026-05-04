package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
	"github.com/ghbvf/gocell/runtime/http/devtools"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// minimalProjectMeta returns a *metadata.ProjectMeta with one cell, sufficient
// to exercise buildCellDepGraph without triggering validation errors from nil
// fields inside governance.DependencyChecker.
func minimalProjectMeta() *metadata.ProjectMeta {
	const cellID = "testcell"

	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellID: {
				ID:   cellID,
				File: "cells/" + cellID + "/cell.yaml",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
	}
}

// minimalPkgGraph returns a minimal *kerneldepgraph.Graph for use in tests.
func minimalPkgGraph() *kerneldepgraph.Graph {
	return kerneldepgraph.FromNodes("github.com/ghbvf/gocell", []*kerneldepgraph.Node{
		{ID: "github.com/ghbvf/gocell/kernel/cell", Layer: "kernel", Imports: []string{}},
	})
}

// TestPhase5InitDevtoolsHandler_NilMeta verifies that when b.devtoolsMeta is nil
// (WithDevtoolsCatalog not called, or called with nil pm), the handler field
// remains nil — endpoint silently absent, no panic.
func TestPhase5InitDevtoolsHandler_NilMeta(t *testing.T) {
	t.Parallel()
	b := New(WithClock(clock.Real()))
	// b.devtoolsMeta is nil by default.

	_, s := newPhaseState()
	err := b.phase5InitDevtoolsHandler(context.Background(), s)

	require.NoError(t, err)
	assert.Nil(t, s.devtoolsHandler)
}

// TestPhase5InitDevtoolsHandler_WithMeta verifies that when pm is non-nil and
// pkgGraph is nil, the handler is constructed with nil pkgGraph.
func TestPhase5InitDevtoolsHandler_WithMeta(t *testing.T) {
	t.Parallel()
	pm := minimalProjectMeta()
	b := New(WithClock(clock.Real()), WithDevtoolsCatalog(pm, "/tmp/test", nil))

	_, s := newPhaseState()
	err := b.phase5InitDevtoolsHandler(context.Background(), s)

	require.NoError(t, err)
	assert.NotNil(t, s.devtoolsHandler, "handler must be non-nil when meta is provided")
}

// TestPhase5InitDevtoolsHandler_WithPkgGraph verifies that when both pm and
// pkgGraph are provided, the handler is constructed with the pkgGraph.
func TestPhase5InitDevtoolsHandler_WithPkgGraph(t *testing.T) {
	t.Parallel()
	pm := minimalProjectMeta()
	b := New(WithClock(clock.Real()), WithDevtoolsCatalog(pm, "/tmp/test", minimalPkgGraph()))

	_, s := newPhaseState()
	err := b.phase5InitDevtoolsHandler(context.Background(), s)

	require.NoError(t, err)
	assert.NotNil(t, s.devtoolsHandler, "handler must be non-nil when meta is provided")
}

// TestBuildCellDepGraph_Empty verifies that an empty (no cells) ProjectMeta
// produces a CellDepGraph with empty Nodes and nil/empty Edges.
func TestBuildCellDepGraph_Empty(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
	}
	g := buildCellDepGraph(pm, clock.Real())

	require.NotNil(t, g)
	assert.Empty(t, g.Nodes)
	assert.Empty(t, g.Edges)
	assert.NotEmpty(t, g.BuiltAt, "BuiltAt must be stamped")
}

// TestBuildCellDepGraph_Sorted verifies that multi-cell ProjectMeta produces
// a deterministically sorted CellDepGraph (Nodes alphabetically, Edges by
// From then To).
func TestBuildCellDepGraph_Sorted(t *testing.T) {
	t.Parallel()
	// Two isolated cells: no slices so no edges — just verifies node sort.
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"zcell": {ID: "zcell", File: "cells/zcell/cell.yaml"},
			"acell": {ID: "acell", File: "cells/acell/cell.yaml"},
			"mcell": {ID: "mcell", File: "cells/mcell/cell.yaml"},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
	}
	g := buildCellDepGraph(pm, clock.Real())

	require.NotNil(t, g)
	require.Len(t, g.Nodes, 3)
	assert.Equal(t, []string{"acell", "mcell", "zcell"}, g.Nodes, "nodes must be sorted alphabetically")
	assert.Empty(t, g.Edges, "no slices → no edges")
	assert.NotEmpty(t, g.BuiltAt, "BuiltAt must be stamped")
}

// TestPhase5CollectRouteGroups_AppendsDevtools verifies that when
// s.devtoolsHandler is non-nil, phase5CollectRouteGroups appends a RouteGroup
// targeting PrimaryListener.
func TestPhase5CollectRouteGroups_AppendsDevtools(t *testing.T) {
	t.Parallel()
	b := New(WithClock(clock.Real()))
	s := buildPhase5State(t)

	// Install a devtools handler on the phaseState to simulate phase5InitDevtoolsHandler.
	pm := minimalProjectMeta()
	s.devtoolsHandler = devtools.NewHandler(pm, nil, nil, "/tmp", clock.Real())

	routers := map[cell.ListenerRef]*router.Router{
		cell.PrimaryListener: buildRouter(t, cell.PrimaryListener),
		cell.HealthListener:  buildRouter(t, cell.HealthListener),
	}

	groups := b.phase5CollectRouteGroups(s, routers)

	// Find the devtools route group.
	var found bool
	for _, rg := range groups {
		if rg.Listener == cell.PrimaryListener && rg.CellID == "_devtools" {
			// Devtools group: Register != nil, Listener == PrimaryListener,
			// CellID == "_devtools" sentinel (framework-injected; SEC-01 fix
			// gives metrics audit attribution to the devtools surface).
			if rg.Register != nil {
				found = true
			}
		}
	}
	// At least one non-health PrimaryListener group with Register != nil must exist.
	// The devtools RouteGroup targets PrimaryListener with the "_devtools" sentinel cell ID.
	require.True(t, found, "devtools RouteGroup targeting PrimaryListener with CellID=_devtools must be present")
}

// TestPhase5CollectRouteGroups_NoDevtools verifies that when devtoolsHandler
// is nil, no extra groups are added beyond health groups.
func TestPhase5CollectRouteGroups_NoDevtools(t *testing.T) {
	t.Parallel()
	b := New(WithClock(clock.Real()))
	s := buildPhase5State(t)
	// s.devtoolsHandler is nil by default.

	routers := map[cell.ListenerRef]*router.Router{
		cell.HealthListener:  buildRouter(t, cell.HealthListener),
		cell.PrimaryListener: buildRouter(t, cell.PrimaryListener),
	}

	withoutDevtools := b.phase5CollectRouteGroups(s, routers)

	baselineCount := len(withoutDevtools)
	assert.Greater(t, baselineCount, 0, "health groups must always be present")

	pm := minimalProjectMeta()
	s.devtoolsHandler = devtools.NewHandler(pm, nil, nil, "/tmp", clock.Real())
	withHandler := b.phase5CollectRouteGroups(s, routers)
	assert.Len(t, withHandler, baselineCount+1,
		"installing devtools handler must append exactly one route group")
}

// Ensure catalog package types compile correctly in bootstrap test context.
var _ *catalog.CellDepGraph = (*catalog.CellDepGraph)(nil)
