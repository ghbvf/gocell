package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles ---

// okCellModule returns a cell.Cell and no error.
type okCellModule struct{ name string }

func (m okCellModule) ID() string { return m.name }

func (m okCellModule) Provide(_ context.Context, _ *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	c := cell.NewBaseCell(cell.CellMetadata{ID: m.name})
	return c, nil, nil
}

var _ CellModule = okCellModule{}

// errCellModule returns an error from Provide.
type errCellModule struct {
	name string
	err  error
}

func (m errCellModule) ID() string { return m.name }

func (m errCellModule) Provide(_ context.Context, _ *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	return nil, nil, m.err
}

var _ CellModule = errCellModule{}

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
	mod := errCellModule{name: "config-core", err: errors.New("db pool unavailable")}
	_, _, err := BuildApp(context.Background(), shared, mod)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config-core")
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
