package composition

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// stubCell creates a minimal cell.Cell implementation using BaseCell.
func stubCell(id string) cell.Cell {
	meta := &metadata.CellMeta{}
	meta.ID = id
	return cell.MustNewBaseCell(meta)
}

// fakeCellModule is a test double for CellModule.
type fakeCellModule struct {
	id         string
	cell       cell.Cell
	opts       []bootstrap.Option
	mres       []kernellifecycle.ManagedResource
	provideErr error
	called     bool
}

func (f *fakeCellModule) ID() string { return f.id }
func (f *fakeCellModule) Provide(
	_ context.Context, _ *SharedDeps,
) (ModuleResult, error) {
	f.called = true
	if f.provideErr != nil {
		return ModuleResult{}, f.provideErr
	}
	return ModuleResult{Cell: f.cell, Opts: f.opts, Resources: f.mres}, nil
}

// closedOrder captures the sequence of Close calls for LIFO verification.
type closedOrder struct {
	calls []string
}

// orderedMR records its close in an external closedOrder.
// It satisfies kernellifecycle.ManagedResource.
type orderedMR struct {
	id    string
	order *closedOrder
}

func (o *orderedMR) Probes() []healthz.Probe { return nil }
func (o *orderedMR) Worker() worker.Worker   { return nil }
func (o *orderedMR) Close(_ context.Context) error {
	o.order.calls = append(o.order.calls, o.id)
	return nil
}

// TestBuilder_HappyPath verifies that cells are assembled in order and
// allOpts = runtimeOpts ++ cellOpts as specified.
func TestBuilder_HappyPath(t *testing.T) {
	ctx := context.Background()

	c1 := stubCell("mod1")
	c2 := stubCell("mod2")

	m1 := &fakeCellModule{id: "mod1", cell: c1}
	m2 := &fakeCellModule{id: "mod2", cell: c2}

	var runtimeCells []cell.Cell
	rtFn := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		runtimeCells = cells
		return nil, nil
	}

	shared := minimalSharedDeps(t)
	app, err := New("mod1", "mod2").With(m1, m2).Build(ctx, shared, rtFn)
	require.NoError(t, err)
	require.NotNil(t, app)

	// runtimeFn received cells in module order.
	require.Len(t, runtimeCells, 2)
	assert.Equal(t, "mod1", runtimeCells[0].ID())
	assert.Equal(t, "mod2", runtimeCells[1].ID())

	assert.True(t, m1.called)
	assert.True(t, m2.called)
}

// TestBuilder_WithDeploymentTopology_NonEmptySpecInSharedDeps verifies that
// Builder.Build correctly injects the non-empty DeploymentTopology from
// SharedDeps via WithDeploymentTopology (F7). We confirm this by reading the
// spec back from SharedDeps after Build (it must be unchanged) and by checking
// the built App opts count reflects the always-injected framework opts.
// The end-to-end sealed-and-queryable assertion is covered in
// runtime/bootstrap TestBootstrap_DeploymentTopologyGetter_BeforePhase0 which
// can call the package-internal phase0ValidateOptions.
func TestBuilder_WithDeploymentTopology_NonEmptySpecInSharedDeps(t *testing.T) {
	ctx := context.Background()

	// SharedDeps with a NON-empty DeploymentTopology (one remote cell).
	shared := minimalSharedDeps(t)
	shared.DeploymentTopology = bootstrap.DeploymentTopologySpec{
		Colocated: []string{"cellA"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "cellB", Endpoint: "cell-b:9090"}},
	}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1}

	app, err := New("mod1").With(m1).Build(ctx, shared,
		func([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
	require.NoError(t, err)
	require.NotNil(t, app)

	// The spec on SharedDeps must be preserved (Build must not mutate it).
	assert.Equal(t, "cellA", shared.DeploymentTopology.Colocated[0])
	require.Len(t, shared.DeploymentTopology.Remote, 1)
	assert.Equal(t, "cellB", shared.DeploymentTopology.Remote[0].CellID,
		"SharedDeps.DeploymentTopology.Remote[0].CellID must flow through Builder.Build unchanged")
	assert.Equal(t, "cell-b:9090", shared.DeploymentTopology.Remote[0].Endpoint,
		"SharedDeps.DeploymentTopology.Remote[0].Endpoint must flow through Builder.Build unchanged")

	// app.opts always contains WithControlPlaneTopology + WithDeploymentTopology
	// (2 framework opts). With one resource (none in this test) + 0 cellOpts +
	// 0 runtimeOpts, the total must be exactly 2.
	assert.Len(t, app.opts, 2,
		"Builder.Build always injects WithControlPlaneTopology + WithDeploymentTopology (2 framework opts)")
}

// TestBuilder_HappyPath_SingleSourceResourceContract verifies the single-source
// resource ownership contract (PR #591 / #1420):
//   - A module returns its ManagedResources ONLY in ModuleResult.Resources. It
//     does NOT call bootstrap.WithManagedResource itself.
//   - Build derives BOTH channels from that one source: it appends one
//     bootstrap.WithManagedResource(r) per resource to cellOpts (steady-state
//     lifecycle, owned by bootstrap.Run) AND appends r to the provisional
//     rollback stack. The two can never diverge — the former double-write bug
//     where a module forgot one half is now structurally impossible.
//
// Mirrors cellmodules/accesscore: the rate limiter is returned only in
// Resources; the Builder, not the module, registers it for steady-state and
// for pre-Run rollback.
func TestBuilder_HappyPath_SingleSourceResourceContract(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{
		id:   "mod1",
		cell: c1,
		// Single source: resource declared ONLY in Resources; no opts. The
		// module does NOT call bootstrap.WithManagedResource (banned in
		// cellmodules/ by WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01).
		mres: []kernellifecycle.ManagedResource{r1},
	}

	app, err := New("mod1").With(m1).Build(ctx, minimalSharedDeps(t),
		func([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
	require.NoError(t, err)
	require.NotNil(t, app)

	// Build derived exactly one WithManagedResource opt from the single Resources
	// entry — the module supplied no opts of its own — plus the two framework
	// opts Build always injects (WithControlPlaneTopology, #1410 review F1; and
	// WithDeploymentTopology, #1962). Three total: 1 resource-derived + 2 framework.
	// A double-write of the resource (the bug this test guards) would make it 4.
	assert.Len(t, app.opts, 3,
		"Builder derives one WithManagedResource opt from ModuleResult.Resources (single source) "+
			"plus the always-injected WithControlPlaneTopology and WithDeploymentTopology framework opts")
	// Success path never rolls back.
	assert.Empty(t, order.calls, "no resource may be Closed on the success path")
}

// TestBuilder_SingleSourceResource_RollsBack verifies the rollback half of the
// single-source contract: a resource returned only in ModuleResult.Resources is
// closed (LIFO) when a later module fails — without the module ever calling
// WithManagedResource. This pairs with the happy-path test above to prove BOTH
// channels are derived from the one Resources source.
func TestBuilder_SingleSourceResource_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}
	m2 := &fakeCellModule{id: "mod2", provideErr: errors.New("provide failed")}

	_, err := New("mod1", "mod2").With(m1, m2).Build(ctx, minimalSharedDeps(t),
		func([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
	require.Error(t, err)

	require.Len(t, order.calls, 1, "the single-source resource must be rolled back on failure")
	assert.Equal(t, "r1", order.calls[0])
}

// TestBuilder_NilModule_RollsBack verifies LIFO rollback when a nil module is encountered.
func TestBuilder_NilModule_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}
	r2 := &orderedMR{id: "r2", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1, r2}}

	shared := minimalSharedDeps(t)
	_, err := New("mod1").With(m1, nil).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")

	// LIFO: r2 appended after r1, so r2 closed first.
	require.Len(t, order.calls, 2)
	assert.Equal(t, "r2", order.calls[0], "LIFO: last appended closed first")
	assert.Equal(t, "r1", order.calls[1])
}

// TestBuilder_ProvideError_RollsBack verifies LIFO rollback on Provide error.
func TestBuilder_ProvideError_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}
	r2 := &orderedMR{id: "r2", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1, r2}}
	m2 := &fakeCellModule{id: "mod2", provideErr: errors.New("provide failed")}

	shared := minimalSharedDeps(t)
	_, err := New("mod1", "mod2").With(m1, m2).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provide failed")

	// LIFO rollback.
	require.Len(t, order.calls, 2)
	assert.Equal(t, "r2", order.calls[0])
	assert.Equal(t, "r1", order.calls[1])
}

// TestBuilder_NilCell_RollsBack verifies rollback when a module returns a nil Cell.
func TestBuilder_NilCell_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}
	m2 := &fakeCellModule{id: "mod2", cell: nil} // nil cell

	shared := minimalSharedDeps(t)
	_, err := New("mod1", "mod2").With(m1, m2).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod2")

	require.Len(t, order.calls, 1)
	assert.Equal(t, "r1", order.calls[0])
}

// TestBuilder_NilResource_FailFast verifies that a module returning a
// nil/typed-nil ManagedResource is rejected at Build time (fail-fast) instead of
// panicking on the rollback path. Before this guard, the steady-state path
// (managedResourceOpts → WithManagedResource) caught nil at phase0, but the
// pre-Run rollback called Close() directly on the provisional stack and would
// panic on a nil interface (bare nil) or nil receiver (typed nil). The validation
// keeps the resource out of the provisional stack entirely, so a prior module's
// valid resource still rolls back cleanly and Build returns a descriptive error.
func TestBuilder_NilResource_FailFast(t *testing.T) {
	ctx := context.Background()

	var typedNil *orderedMR // typed-nil: non-nil interface wrapping a nil *orderedMR

	tests := []struct {
		name string
		bad  kernellifecycle.ManagedResource
	}{
		{name: "bare nil", bad: nil},
		{name: "typed nil", bad: typedNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			order := &closedOrder{}
			r1 := &orderedMR{id: "r1", order: order}

			c1 := stubCell("mod1")
			m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}
			c2 := stubCell("mod2")
			m2 := &fakeCellModule{id: "mod2", cell: c2, mres: []kernellifecycle.ManagedResource{tc.bad}}

			// Must NOT panic; must return a fail-fast error naming the module.
			_, err := New("mod1", "mod2").With(m1, m2).Build(ctx, minimalSharedDeps(t),
				func([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mod2")
			assert.Contains(t, err.Error(), "nil ManagedResource")

			// The prior module's valid resource rolled back without panic; the
			// nil resource never entered the provisional stack.
			require.Len(t, order.calls, 1, "prior valid resource must roll back")
			assert.Equal(t, "r1", order.calls[0])
		})
	}
}

// TestBuilder_RuntimeFnError_RollsBack verifies rollback when the RuntimeOptionsFunc errors.
func TestBuilder_RuntimeFnError_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}

	shared := minimalSharedDeps(t)
	_, err := New("mod1").With(m1).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, errors.New("runtime fn error")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime fn error")

	require.Len(t, order.calls, 1)
	assert.Equal(t, "r1", order.calls[0])
}

// TestBuilder_NilSharedDeps verifies that Build rejects a nil *SharedDeps.
func TestBuilder_NilSharedDeps(t *testing.T) {
	ctx := context.Background()
	_, err := New().Build(ctx, nil, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
}

// TestBuilder_UnsealedSharedDeps_Rejected verifies the sealed-construction
// invariant: a *SharedDeps not produced by NewSharedDeps (no validity marker)
// is rejected by Build even if all its fields are populated.
func TestBuilder_UnsealedSharedDeps_Rejected(t *testing.T) {
	ctx := context.Background()
	// Bare literal: exported fields can be set, but the unexported marker cannot,
	// so Build must reject it.
	bare := &SharedDeps{}
	_, err := New().Build(ctx, bare, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NewSharedDeps")
}

// TestBuilder_MutatedAfterSeal_Rejected verifies that Build re-validates the
// dependency set at Build time, not just the sealed-construction marker: a
// *SharedDeps produced by NewSharedDeps (marker set) but whose required field is
// subsequently nulled out must be rejected. Without the Build-time validate()
// call, the stale valid marker would let a broken dep set through (F1 / cluster
// C1). Mirrors Kubernetes CompletedOptions.Validate() guarding pre-startup state.
func TestBuilder_MutatedAfterSeal_Rejected(t *testing.T) {
	ctx := context.Background()
	shared := minimalSharedDeps(t) // sealed + valid
	// Mutate a required exported field to a broken state after sealing. The
	// unexported valid marker stays true.
	shared.JWTVerifier = nil
	require.True(t, shared.valid, "precondition: marker still set after mutation")

	c1 := stubCell("mod1")
	m1 := &fakeCellModule{id: "mod1", cell: c1}
	_, err := New("mod1").With(m1).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid at Build time")
	assert.False(t, m1.called, "no module Provide may run when Build inputs are invalid")
}

// TestBuilder_NilRuntimeOptsFn_Rejected verifies Build returns an error (not a
// panic) when runtimeOptsFn is nil — error-first public API.
func TestBuilder_NilRuntimeOptsFn_Rejected(t *testing.T) {
	ctx := context.Background()
	shared := minimalSharedDeps(t)
	_, err := New().Build(ctx, shared, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtimeOptsFn")
}

// TestBuilder_With_Accumulates verifies that With() calls accumulate modules.
func TestBuilder_With_Accumulates(t *testing.T) {
	c1 := stubCell("m1")
	c2 := stubCell("m2")
	m1 := &fakeCellModule{id: "m1", cell: c1}
	m2 := &fakeCellModule{id: "m2", cell: c2}

	b := New().With(m1).With(m2)
	assert.Len(t, b.modules, 2)
}
