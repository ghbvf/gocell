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
	_ context.Context, _ *SharedDeps, _ ModuleExports,
) (cell.Cell, ModuleExports, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	f.called = true
	if f.provideErr != nil {
		return nil, ModuleExports{}, nil, nil, f.provideErr
	}
	return f.cell, ModuleExports{}, f.opts, f.mres, nil
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

	c1 := stubCell("cell-1")
	c2 := stubCell("cell-2")

	m1 := &fakeCellModule{id: "mod1", cell: c1}
	m2 := &fakeCellModule{id: "mod2", cell: c2}

	var runtimeCells []cell.Cell
	rtFn := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		runtimeCells = cells
		return nil, nil
	}

	shared := minimalSharedDeps(t)
	app, err := New().With(m1, m2).Build(ctx, shared, rtFn)
	require.NoError(t, err)
	require.NotNil(t, app)

	// runtimeFn received cells in module order.
	require.Len(t, runtimeCells, 2)
	assert.Equal(t, "cell-1", runtimeCells[0].ID())
	assert.Equal(t, "cell-2", runtimeCells[1].ID())

	assert.True(t, m1.called)
	assert.True(t, m2.called)
}

// TestBuilder_HappyPath_TwoChannelResourceContract verifies the two-channel
// resource ownership contract (pg-cell-template Chapter 4):
//   - A module's bootstrap.Option (3rd return, e.g. bootstrap.WithManagedResource)
//     flows into App.opts so bootstrap.Run owns the steady-state lifecycle.
//   - A module's []ManagedResource (4th return) is rollback-only: Build must NOT
//     additionally convert it into bootstrap opts on the success path, or the
//     resource would be registered twice (the module already registered it via
//     its own opts) and double-closed at shutdown.
//
// Mirrors cellmodules/accesscore: WithManagedResource(limiter) in opts +
// the same limiter in the 4th channel for pre-Run rollback.
func TestBuilder_HappyPath_TwoChannelResourceContract(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("cell-1")
	m1 := &fakeCellModule{
		id:   "mod1",
		cell: c1,
		opts: []bootstrap.Option{bootstrap.WithManagedResource(r1)},
		mres: []kernellifecycle.ManagedResource{r1},
	}

	app, err := New().With(m1).Build(ctx, minimalSharedDeps(t),
		func([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
	require.NoError(t, err)
	require.NotNil(t, app)

	// Exactly the module's own opt is present — the 4th-channel resource is NOT
	// additionally threaded (that would double-register against the module's
	// own WithManagedResource).
	assert.Len(t, app.opts, 1,
		"module opts flow through; Builder must not also convert mres into opts")
	// Success path never rolls back.
	assert.Empty(t, order.calls, "no resource may be Closed on the success path")
}

// TestBuilder_NilModule_RollsBack verifies LIFO rollback when a nil module is encountered.
func TestBuilder_NilModule_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}
	r2 := &orderedMR{id: "r2", order: order}

	c1 := stubCell("cell-1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1, r2}}

	shared := minimalSharedDeps(t)
	_, err := New().With(m1, nil).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
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

	c1 := stubCell("cell-1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1, r2}}
	m2 := &fakeCellModule{id: "mod2", provideErr: errors.New("provide failed")}

	shared := minimalSharedDeps(t)
	_, err := New().With(m1, m2).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
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

	c1 := stubCell("cell-1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}
	m2 := &fakeCellModule{id: "mod2", cell: nil} // nil cell

	shared := minimalSharedDeps(t)
	_, err := New().With(m1, m2).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
		return nil, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod2")

	require.Len(t, order.calls, 1)
	assert.Equal(t, "r1", order.calls[0])
}

// TestBuilder_RuntimeFnError_RollsBack verifies rollback when the RuntimeOptionsFunc errors.
func TestBuilder_RuntimeFnError_RollsBack(t *testing.T) {
	ctx := context.Background()

	order := &closedOrder{}
	r1 := &orderedMR{id: "r1", order: order}

	c1 := stubCell("cell-1")
	m1 := &fakeCellModule{id: "mod1", cell: c1, mres: []kernellifecycle.ManagedResource{r1}}

	shared := minimalSharedDeps(t)
	_, err := New().With(m1).Build(ctx, shared, func([]cell.Cell) ([]bootstrap.Option, error) {
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
	c1 := stubCell("c1")
	c2 := stubCell("c2")
	m1 := &fakeCellModule{id: "m1", cell: c1}
	m2 := &fakeCellModule{id: "m2", cell: c2}

	b := New().With(m1).With(m2)
	assert.Len(t, b.modules, 2)
}
