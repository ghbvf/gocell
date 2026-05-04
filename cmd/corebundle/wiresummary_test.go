package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// TestBuildCellWireSummaries_NoCellGo verifies that a project where no cell
// has a cell.go returns a non-error result. Merge produces an empty WireBundle
// for cells without cell.go; DeriveCellWireSummaries emits one summary per
// bundle entry with empty Listeners/Routes/Subscribes.
func TestBuildCellWireSummaries_NoCellGo(t *testing.T) {
	root := buildWireSummaryFixture(t)

	pm := buildWireSummaryProjectMeta(t, root)
	summaries, err := BuildCellWireSummaries(root, pm)
	require.NoError(t, err)

	require.Len(t, summaries, 1, "one summary per cell")
	assert.Equal(t, "wirecell", summaries[0].CellID)
	assert.Empty(t, summaries[0].Listeners)
	assert.Empty(t, summaries[0].Routes)
	assert.Empty(t, summaries[0].Subscribes)
}

// TestBuildCellWireSummaries_WithMarkers verifies that a cell with a valid
// cell.go containing markers produces non-empty summary fields.
func TestBuildCellWireSummaries_WithMarkers(t *testing.T) {
	root := buildWireSummaryFixture(t)

	// Write a minimal cell.go with a listener marker.
	cellDir := filepath.Join(root, "cells", "wirecell")
	cellGoContent := `package wirecell

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/wire
type WireCell struct{}
`
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.go"), []byte(cellGoContent), 0o600))

	pm := buildWireSummaryProjectMeta(t, root)
	summaries, err := BuildCellWireSummaries(root, pm)
	require.NoError(t, err)

	require.Len(t, summaries, 1)
	assert.Equal(t, "wirecell", summaries[0].CellID)
	require.Len(t, summaries[0].Listeners, 1, "one listener marker")
	assert.Equal(t, "cell.PrimaryListener", summaries[0].Listeners[0].Ref)
	assert.Equal(t, "/api/v1/wire", summaries[0].Listeners[0].Prefix)
}

// TestBuildCellWireSummaries_NilProject verifies that a nil project is handled
// gracefully (no panic). markergen.Merge iterates project.Cells, so a nil
// project produces a nil-dereference unless the caller guards — this test
// confirms the function is safe.
func TestBuildCellWireSummaries_NilProject(t *testing.T) {
	root := t.TempDir()
	// nil project → markergen.Merge will panic on project.Cells iteration;
	// wrapping in a recover ensures we catch that if it happens.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("BuildCellWireSummaries panicked on nil project: %v", r)
			}
		}()
		summaries, err := BuildCellWireSummaries(root, nil)
		// Either an error or empty slice is acceptable.
		if err == nil {
			assert.Empty(t, summaries)
		}
	}()
}

// TestWireBundleToCellWireBundle_AllFields verifies the field-by-field
// conversion from markergen.WireBundle → metadata.CellWireBundle.
func TestWireBundleToCellWireBundle_AllFields(t *testing.T) {
	wb := markergen.WireBundle{
		Listeners: []markergen.ListenerSpec{
			{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
		},
		Routes: []markergen.RouteSpec{
			{Slice: "myslice", Listener: "cell.PrimaryListener", SubPath: "/sessions", Method: "RegisterRoutes"},
		},
		Subscribes: []markergen.SubscribeSpec{
			{Slice: "myslice", Topic: "my.topic.v1", Handler: "HandleEvent", Group: "cg-myservice-event"},
		},
	}

	cb := wireBundleToCellWireBundle(wb)

	require.Len(t, cb.Listeners, 1)
	assert.Equal(t, "cell.PrimaryListener", cb.Listeners[0].Ref)
	assert.Equal(t, "/api/v1", cb.Listeners[0].Prefix)

	require.Len(t, cb.Routes, 1)
	assert.Equal(t, "myslice", cb.Routes[0].Slice)
	assert.Equal(t, "cell.PrimaryListener", cb.Routes[0].Listener)
	assert.Equal(t, "/sessions", cb.Routes[0].SubPath)
	assert.Equal(t, "RegisterRoutes", cb.Routes[0].Method)

	require.Len(t, cb.Subscribes, 1)
	assert.Equal(t, "myslice", cb.Subscribes[0].Slice)
	assert.Equal(t, "my.topic.v1", cb.Subscribes[0].Topic)
	assert.Equal(t, "HandleEvent", cb.Subscribes[0].Handler)
	assert.Equal(t, "cg-myservice-event", cb.Subscribes[0].Group)
}

// TestWireBundleToCellWireBundle_EmptyBundle verifies that an empty WireBundle
// produces a CellWireBundle with empty (non-panic) slices.
func TestWireBundleToCellWireBundle_EmptyBundle(t *testing.T) {
	cb := wireBundleToCellWireBundle(markergen.WireBundle{})
	assert.Empty(t, cb.Listeners)
	assert.Empty(t, cb.Routes)
	assert.Empty(t, cb.Subscribes)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildWireSummaryFixture creates a minimal GoCell fixture directory with
// one cell (no cell.go) and returns the root path.
func buildWireSummaryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/wiresummary\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "actors.yaml"),
		[]byte("# no actors\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "journeys", "status-board.yaml"),
		[]byte("# no entries\n"), 0o644))

	cellDir := filepath.Join(root, "cells", "wirecell")
	sliceDir := filepath.Join(cellDir, "slices", "wireslice")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))

	cellYAML := `id: wirecell
type: platform
consistencyLevel: L1
owner:
  team: test
  role: owner
schema:
  primary: wirecell
verify:
  smoke: []
`
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o600))

	sliceYAML := `id: wireslice
belongsToCell: wirecell
consistencyLevel: L1
contractUsages: []
verify:
  unit: []
  contract: []
allowedFiles:
  - "*.go"
`
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o600))
	return root
}

// buildWireSummaryProjectMeta parses a minimal ProjectMeta from root.
func buildWireSummaryProjectMeta(t *testing.T, root string) *metadata.ProjectMeta {
	t.Helper()
	pm, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)
	return pm
}
