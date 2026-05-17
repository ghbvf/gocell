package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// --- test doubles ---

// okCellModule returns a cell.Cell and no error.
type okCellModule struct{ name string }

func (m okCellModule) ID() string { return m.name }

func (m okCellModule) Provide(_ context.Context, _ *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: m.name, DurabilityMode: "demo"})
	return c, nil, nil, nil
}

var _ CellModule = okCellModule{}

// errCellModule returns an error from Provide.
type errCellModule struct {
	name string
	err  error
}

func (m errCellModule) ID() string { return m.name }

func (m errCellModule) Provide(_ context.Context, _ *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	return nil, nil, nil, m.err
}

var _ CellModule = errCellModule{}

// resourceCellModule is a CellModule that returns a tracked ManagedResource.
// Used to verify BuildApp rollback behavior.
type resourceCellModule struct {
	name string
	res  kernellifecycle.ManagedResource
}

func (m resourceCellModule) ID() string { return m.name }

func (m resourceCellModule) Provide(
	_ context.Context, _ *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: m.name, DurabilityMode: "demo"})
	var res []kernellifecycle.ManagedResource
	if m.res != nil {
		res = append(res, m.res)
	}
	return c, nil, res, nil
}

var _ CellModule = resourceCellModule{}

// trackingManagedResource records whether Close was called.
type trackingManagedResource struct {
	name        string
	closeCalled bool
}

func (r *trackingManagedResource) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{r.name: func(context.Context) error { return nil }}
}

func (r *trackingManagedResource) Worker() kworker.Worker { return nil }

func (r *trackingManagedResource) Close(_ context.Context) error {
	r.closeCalled = true
	return nil
}

var _ kernellifecycle.ManagedResource = (*trackingManagedResource)(nil)

// --- tests ---

// TestBuildApp_NilSharedDeps verifies that nil shared deps is rejected.
func TestBuildApp_NilSharedDeps(t *testing.T) {
	_, _, err := BuildApp(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil")
}

// TestBuildApp_ValidationFail verifies that shared.Validate() failure is
// propagated and includes "validation" in the error message.
func TestBuildApp_ValidationFail(t *testing.T) {
	// A zero-value SharedDeps fails Validate() because all required fields are nil.
	_, _, err := BuildApp(context.Background(), &SharedDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation")
}

// TestBuildApp_ModuleProvideError verifies that a module Provide error is
// propagated and includes the module ID.
func TestBuildApp_ModuleProvideError(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)
	mod := errCellModule{name: "configcore", err: errors.New("db pool unavailable")}
	_, _, err := BuildApp(context.Background(), shared, mod)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configcore")
}

// TestBuildApp_NilModuleInList verifies that a nil entry in the module list
// causes BuildApp to return an error.
func TestBuildApp_NilModuleInList(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)
	_, _, err := BuildApp(context.Background(), shared, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestBuildApp_EmptyModuleList verifies that zero modules is a valid call:
// BuildApp returns empty cells/opts slices and no error.
func TestBuildApp_EmptyModuleList(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)
	cells, opts, err := BuildApp(context.Background(), shared)
	require.NoError(t, err)
	assert.Empty(t, cells)
	assert.Empty(t, opts)
}

// nilCellModule is a CellModule whose Provide always returns a nil Cell.
// It is used to test that BuildApp rejects nil Cell returns as a fail-fast error.
type nilCellModule struct{}

func (m nilCellModule) ID() string { return "nil-cell-module" }

func (m nilCellModule) Provide(_ context.Context, _ *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	var nilCell cell.Cell
	return nilCell, []bootstrap.Option{}, []kernellifecycle.ManagedResource{}, nil
}

var _ CellModule = nilCellModule{}

// TestBuildApp_RejectsNilCell verifies that BuildApp returns an error when a
// module's Provide returns a nil Cell. ref: uber-fx, kubernetes, kratos —
// required assembly components must fail-fast, not be silently skipped.
func TestBuildApp_RejectsNilCell(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)

	cells, opts, err := BuildApp(context.Background(), shared, nilCellModule{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "returned nil Cell")
	require.Contains(t, err.Error(), "nil-cell-module")
	require.Nil(t, cells)
	require.Nil(t, opts)
}

// TestBuildApp_TwoModules_AggregateCells verifies that BuildApp collects cells
// from all modules in order.
func TestBuildApp_TwoModules_AggregateCells(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)
	modA := okCellModule{name: "cell-a"}
	modB := okCellModule{name: "cell-b"}
	cells, _, err := BuildApp(context.Background(), shared, modA, modB)
	require.NoError(t, err)
	require.Len(t, cells, 2)
	assert.Equal(t, "cell-a", cells[0].ID())
	assert.Equal(t, "cell-b", cells[1].ID())
}

// TestBuildApp_PartialFailure_CleansUpManagedResource verifies that when a
// subsequent module's Provide fails, resources registered by earlier modules
// are closed in reverse order to prevent resource leaks.
func TestBuildApp_PartialFailure_CleansUpManagedResource(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	shared := buildTestSharedDeps(t)

	res := &trackingManagedResource{name: "fake-pg"}
	modA := resourceCellModule{name: "cell-a", res: res}
	modB := errCellModule{name: "cell-b", err: errors.New("db pool unavailable")}

	_, _, err := BuildApp(context.Background(), shared, modA, modB)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cell-b")
	assert.True(t, res.closeCalled, "resource from module A must be closed when module B's Provide fails")
}

// TestCorebundleModules_ConfigCoreBeforeAccessCore asserts the ordering invariant
// documented in access_module.go line 91-92:
//
//	"ConfigCoreModule must run before AccessCoreModule"
//
// AccessCoreModule.Provide reads shared.SharedPGPool which is populated by
// ConfigCoreModule. If the modules were reordered, accesscore would start
// against a nil pool and fail at runtime rather than at construction.
// This test will fail if a future refactor reorders generatedCellModules().
func TestCorebundleModules_ConfigCoreBeforeAccessCore(t *testing.T) {
	mods := generatedCellModules()

	configIdx, accessIdx := -1, -1
	for i, m := range mods {
		switch m.ID() {
		case "configcore":
			configIdx = i
		case "accesscore":
			accessIdx = i
		}
	}

	require.NotEqual(t, -1, configIdx, "configcore module not found in generatedCellModules()")
	require.NotEqual(t, -1, accessIdx, "accesscore module not found in generatedCellModules()")

	assert.Less(t, configIdx, accessIdx,
		"ConfigCoreModule (idx=%d) must appear before AccessCoreModule (idx=%d): "+
			"configcore initializes SharedPGPool which accesscore consumes via shared.SharedPGPool",
		configIdx, accessIdx)
}
