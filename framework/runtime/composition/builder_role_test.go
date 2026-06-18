package composition

// builder_role_test.go — tests for NewForRole, the role-based subset-mount
// constructor (#2278 PR-2). Two branches:
//   - zero spec (monolith): mount ALL modules unfiltered (so M12a still detects
//     an out-of-assembly module — filtering here would silently drop it).
//   - explicit spec (split): mount only the colocated subset; other groups' cells
//     are remote and their modules are dropped.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// TestNewForRole_MonolithMountsAll verifies that a zero (all-colocated) spec
// mounts every module — equivalent to New(assemblyCellIDs...).With(allModules...).
func TestNewForRole_MonolithMountsAll(t *testing.T) {
	ctx := context.Background()
	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	m2 := &fakeCellModule{id: "mod2", cell: stubCell("mod2")}

	var runtimeCells []cell.Cell
	rtFn := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		runtimeCells = cells
		return nil, nil
	}

	app, err := NewForRole([]string{"mod1", "mod2"}, bootstrap.DeploymentTopologySpec{}, m1, m2).
		Build(ctx, minimalSharedDeps(t), rtFn)
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.True(t, m1.called && m2.called, "monolith must mount every module")
	require.Len(t, runtimeCells, 2)
}

// TestNewForRole_SplitMountsColocatedSubset verifies that an explicit spec mounts
// only this role's colocated cells; modules for remote cells are filtered out
// (their Provide is never called).
func TestNewForRole_SplitMountsColocatedSubset(t *testing.T) {
	ctx := context.Background()
	spec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"mod1"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "mod2", Endpoint: "mod2.svc:9090"}},
	}
	// In production shared.DeploymentTopology and the spec passed to NewForRole are
	// the SAME value (both = SpecForRole(generatedTopologyGroups(), role)); the test
	// aligns them explicitly to mirror that single-source contract.
	shared := minimalSharedDeps(t)
	shared.DeploymentTopology = spec

	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	m2 := &fakeCellModule{id: "mod2", cell: stubCell("mod2")}

	var runtimeCells []cell.Cell
	rtFn := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		runtimeCells = cells
		return nil, nil
	}

	app, err := NewForRole([]string{"mod1", "mod2"}, spec, m1, m2).Build(ctx, shared, rtFn)
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.True(t, m1.called, "colocated module mod1 must be mounted")
	assert.False(t, m2.called, "remote module mod2 must NOT be mounted (its cell is remote)")
	require.Len(t, runtimeCells, 1)
	assert.Equal(t, "mod1", runtimeCells[0].ID())
}

// TestNewForRole_SplitMissingColocatedModuleFails verifies that a colocated cell
// with no providing module is rejected by Build's closed-set guard (the filtered
// module set must be a bijection onto the colocated cell IDs).
func TestNewForRole_SplitMissingColocatedModuleFails(t *testing.T) {
	ctx := context.Background()
	spec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"mod1", "mod3"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "mod2", Endpoint: "mod2.svc:9090"}},
	}
	shared := minimalSharedDeps(t)
	shared.DeploymentTopology = spec

	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	m2 := &fakeCellModule{id: "mod2", cell: stubCell("mod2")}

	_, err := NewForRole([]string{"mod1", "mod2", "mod3"}, spec, m1, m2).Build(ctx, shared, noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod3")
	assert.Contains(t, err.Error(), "no module provides it")
}

// TestNewForRole_SplitStrangerModuleRejected verifies that a module whose cell is
// in NEITHER the colocated set nor the remote set (a "stranger" — generatedCellModules
// drifted from the topology groups) is NOT silently dropped: NewForRole drops only
// known-remote modules, so the stranger reaches Build and is rejected by the
// closed-set guard "not in the assembly closed set" (#2278 review).
func TestNewForRole_SplitStrangerModuleRejected(t *testing.T) {
	ctx := context.Background()
	spec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"mod1"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "mod2", Endpoint: "mod2.svc:9090"}},
	}
	shared := minimalSharedDeps(t)
	shared.DeploymentTopology = spec

	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	m2 := &fakeCellModule{id: "mod2", cell: stubCell("mod2")} // remote → dropped
	mStranger := &fakeCellModule{id: "zzz", cell: stubCell("zzz")}

	_, err := NewForRole([]string{"mod1", "mod2"}, spec, m1, m2, mStranger).Build(ctx, shared, noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zzz")
	assert.Contains(t, err.Error(), "closed set")
}

// TestNewForRole_SplitNilModuleFails verifies that a nil module in the split
// branch is NOT silently dropped: NewForRole passes it through to Build, whose
// nil-guard fail-fasts (#2278 review F1) — preserving the same non-nil contract
// plain New() relies on.
func TestNewForRole_SplitNilModuleFails(t *testing.T) {
	ctx := context.Background()
	spec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"mod1"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "mod2", Endpoint: "mod2.svc:9090"}},
	}
	shared := minimalSharedDeps(t)
	shared.DeploymentTopology = spec
	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}

	_, err := NewForRole([]string{"mod1", "mod2"}, spec, m1, nil).Build(ctx, shared, noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "module list contains nil")
}

// TestNewForRole_SpecMismatchSharedFails verifies the single deployment-topology
// source guard (#2278 review F2): if the spec NewForRole filtered with differs
// from SharedDeps.DeploymentTopology that Build seals, Build fail-fasts — the
// mounted subset and the sealed runtime topology must not diverge.
func TestNewForRole_SpecMismatchSharedFails(t *testing.T) {
	ctx := context.Background()
	roleSpec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"mod1"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "mod2", Endpoint: "mod2.svc:9090"}},
	}
	shared := minimalSharedDeps(t)
	// shared carries a DIFFERENT topology than the spec NewForRole filtered with.
	shared.DeploymentTopology = bootstrap.DeploymentTopologySpec{Colocated: []string{"mod1", "mod2"}}
	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}

	_, err := NewForRole([]string{"mod1", "mod2"}, roleSpec, m1).Build(ctx, shared, noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DeploymentTopology")
}

// TestNewForRole_MonolithDoesNotFilter_M12aDriftDetected is the regression guard
// for the M12a trap: the monolith branch must pass ALL modules to Build
// unfiltered, so an out-of-assembly module is caught by the closed-set guard
// rather than silently dropped. If NewForRole filtered in the monolith branch,
// mOutsider would vanish and the drift would go undetected.
func TestNewForRole_MonolithDoesNotFilter_M12aDriftDetected(t *testing.T) {
	ctx := context.Background()
	m1 := &fakeCellModule{id: "mod1", cell: stubCell("mod1")}
	mOutsider := &fakeCellModule{id: "outsider", cell: stubCell("outsider")}

	_, err := NewForRole([]string{"mod1"}, bootstrap.DeploymentTopologySpec{}, m1, mOutsider).
		Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outsider")
	assert.Contains(t, err.Error(), "closed set")
}
