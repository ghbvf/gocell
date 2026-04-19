package bootstrap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles ---

type okSharedDeps struct{}

func (okSharedDeps) Validate() error { return nil }

type failSharedDeps struct{}

func (failSharedDeps) Validate() error {
	return fmt.Errorf("validation: missing required token")
}

// okModule returns a cell.Cell (via cell.BaseCell) and no error.
type okModule struct{ name string }

func (m okModule) ID() string { return m.name }

func (m okModule) Provide(_ context.Context, _ bootstrap.SharedDepsProvider) (cell.Cell, []bootstrap.Option, error) {
	c := cell.NewBaseCell(cell.CellMetadata{ID: m.name})
	return c, nil, nil
}

var _ bootstrap.CellModule = okModule{}

// errModule returns an error from Provide.
type errModule struct {
	name string
	err  error
}

func (m errModule) ID() string { return m.name }

func (m errModule) Provide(_ context.Context, _ bootstrap.SharedDepsProvider) (cell.Cell, []bootstrap.Option, error) {
	return nil, nil, m.err
}

var _ bootstrap.CellModule = errModule{}

// --- tests ---

// TestBuildApp_NilSharedDeps verifies that nil shared deps is rejected.
func TestBuildApp_NilSharedDeps(t *testing.T) {
	_, _, err := bootstrap.BuildApp(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil shared deps")
}

// TestBuildApp_ValidationFail verifies that shared.Validate() failure is
// propagated and includes "validation" in the error message.
func TestBuildApp_ValidationFail(t *testing.T) {
	_, _, err := bootstrap.BuildApp(context.Background(), failSharedDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation")
}

// TestBuildApp_ModuleProvideError verifies that a module Provide error is
// propagated and includes the module ID.
func TestBuildApp_ModuleProvideError(t *testing.T) {
	mod := errModule{name: "config-core", err: errors.New("db pool unavailable")}
	_, _, err := bootstrap.BuildApp(context.Background(), okSharedDeps{}, mod)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config-core")
}

// TestBuildApp_NilModuleInList verifies that a nil entry in the module list
// causes BuildApp to return an error.
func TestBuildApp_NilModuleInList(t *testing.T) {
	_, _, err := bootstrap.BuildApp(context.Background(), okSharedDeps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestBuildApp_EmptyModuleList verifies that zero modules is a valid call:
// BuildApp returns empty cells/opts slices and no error.
func TestBuildApp_EmptyModuleList(t *testing.T) {
	cells, opts, err := bootstrap.BuildApp(context.Background(), okSharedDeps{})
	require.NoError(t, err)
	assert.Empty(t, cells)
	assert.Empty(t, opts)
}

// TestBuildApp_TwoModules_AggregateCells verifies that BuildApp collects cells
// from all modules in order.
func TestBuildApp_TwoModules_AggregateCells(t *testing.T) {
	modA := okModule{name: "cell-a"}
	modB := okModule{name: "cell-b"}
	cells, _, err := bootstrap.BuildApp(context.Background(), okSharedDeps{}, modA, modB)
	require.NoError(t, err)
	require.Len(t, cells, 2)
	assert.Equal(t, "cell-a", cells[0].ID())
	assert.Equal(t, "cell-b", cells[1].ID())
}
