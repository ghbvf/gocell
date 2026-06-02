package composition

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// noopRuntimeOpts is a RuntimeOptionsFunc that adds nothing — used by the
// closed-set tests, which only exercise the pre-Provide closed-set guard.
func noopRuntimeOpts([]cell.Cell) ([]bootstrap.Option, error) { return nil, nil }

// TestBuilder_ClosedSet_RejectsCellOutsideSet verifies the M12a build-time
// guard (#1093): a composed module whose ID is not in the assembly's declared
// cell-id closed set is rejected fail-fast, before any module Provide runs.
func TestBuilder_ClosedSet_RejectsCellOutsideSet(t *testing.T) {
	ctx := context.Background()
	mOutsider := &fakeCellModule{id: "outsider", cell: stubCell("outsider")}

	_, err := New("configcore").With(mOutsider).Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outsider")
	assert.Contains(t, err.Error(), "closed set")
	assert.False(t, mOutsider.called, "out-of-set cell must be rejected before Provide")
}

// TestBuilder_ClosedSet_RejectsMissingDeclaredCell verifies that a cell
// declared in the assembly closed set but not provided by any module is
// rejected (bijection completeness half).
func TestBuilder_ClosedSet_RejectsMissingDeclaredCell(t *testing.T) {
	ctx := context.Background()
	mA := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}

	_, err := New("configcore", "auditcore").With(mA).Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auditcore")
	assert.Contains(t, err.Error(), "no module provides it")
	assert.False(t, mA.called, "missing declared cell must be detected before Provide")
}

// TestBuilder_ClosedSet_RejectsDuplicateModule verifies that two modules
// composing the same cell ID are rejected.
func TestBuilder_ClosedSet_RejectsDuplicateModule(t *testing.T) {
	ctx := context.Background()
	m1 := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}
	m2 := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}

	_, err := New("configcore").With(m1, m2).Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.Contains(t, err.Error(), "configcore")
}

// TestBuilder_ClosedSet_HappyPathBijection verifies that an exact bijection
// (every declared cell provided once, no extras) Builds successfully.
func TestBuilder_ClosedSet_HappyPathBijection(t *testing.T) {
	ctx := context.Background()
	mA := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}
	mB := &fakeCellModule{id: "auditcore", cell: stubCell("auditcore")}

	app, err := New("auditcore", "configcore").With(mA, mB).Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.True(t, mA.called && mB.called, "happy-path bijection must run all module Provides")
}
