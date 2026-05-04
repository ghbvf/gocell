package markergen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// testdataDir returns the absolute path to the testdata directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}

// buildProjectMeta constructs a minimal ProjectMeta for testing without
// touching the real filesystem project.
func buildProjectMeta(cells map[string]*metadata.CellMeta, slices map[string]*metadata.SliceMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:  cells,
		Slices: slices,
	}
}

// TestMerge_MarkerPath tests that Merge reads marker comments from cell.go.
func TestMerge_MarkerPath(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)

	// cell.File points to a yaml inside testdata, so filepath.Dir gives testdata/
	// and Merge will look for testdata/cell.go.  We rename the fixture to "cell.go"
	// in a temp dir.
	tmp := t.TempDir()
	copyFile(t, filepath.Join(td, "cell_withmarkers.go"), filepath.Join(tmp, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"markercell": {
				ID:   "markercell",
				File: "fakedir/cell.yaml", // Dir("fakedir/cell.yaml") → "fakedir"
			},
		},
		map[string]*metadata.SliceMeta{},
	)
	// Override: cell.File dir must match tmp. Adjust manually.
	project.Cells["markercell"].File = filepath.Join(filepath.Base(tmp), "cell.yaml")

	// Merge uses filepath.Join(projectRoot, filepath.Dir(cell.File), "cell.go").
	// projectRoot = parent of tmp, Dir(cell.File) = Base(tmp).
	projectRoot := filepath.Dir(tmp)
	// Re-set cell.File to relative path from projectRoot.
	rel, _ := filepath.Rel(projectRoot, filepath.Join(tmp, "cell.yaml"))
	project.Cells["markercell"].File = rel

	bundles, err := Merge(projectRoot, project)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	bundle, ok := bundles["markercell"]
	if !ok {
		t.Fatal("bundle for markercell not found")
	}
	if len(bundle.Listeners) != 1 || bundle.Listeners[0].Ref != "cell.PrimaryListener" {
		t.Errorf("listeners=%v", bundle.Listeners)
	}
	if len(bundle.Routes) != 1 || bundle.Routes[0].Slice != "sliceA" {
		t.Errorf("routes=%v", bundle.Routes)
	}
	if len(bundle.Subscribes) != 1 || bundle.Subscribes[0].Topic != "event.foo.v1" {
		t.Errorf("subscribes=%v", bundle.Subscribes)
	}
}

// TestMerge_FallbackWhenNoCellGo tests that when cell.go is absent, Merge
// falls back to yaml-derived bundle.
func TestMerge_FallbackWhenNoCellGo(t *testing.T) {
	t.Parallel()
	// cell.File references a dir that has no cell.go sibling.
	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"yamlcell": {
				ID:   "yamlcell",
				File: "cells/yamlcell/cell.yaml",
				Listeners: []metadata.ListenerDeclMeta{
					{Ref: "cell.PrimaryListener", Prefix: "/api/v1"},
				},
			},
		},
		map[string]*metadata.SliceMeta{
			"yamlcell/sliceX": {
				ID:            "sliceX",
				BelongsToCell: "yamlcell",
				RouteMounts: []metadata.RouteMountMeta{
					{Listener: "cell.PrimaryListener", SubPath: "/items", HandlerField: "h"},
				},
			},
		},
	)

	// Use an empty temp dir as projectRoot — cells/yamlcell/cell.go won't exist.
	tmp := t.TempDir()
	bundles, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("Merge fallback: %v", err)
	}
	bundle, ok := bundles["yamlcell"]
	if !ok {
		t.Fatal("bundle for yamlcell not found")
	}
	if len(bundle.Listeners) != 1 || bundle.Listeners[0].Ref != "cell.PrimaryListener" {
		t.Errorf("fallback listeners=%v", bundle.Listeners)
	}
	if len(bundle.Routes) != 1 || bundle.Routes[0].SubPath != "/items" {
		t.Errorf("fallback routes=%v", bundle.Routes)
	}
}

// TestMerge_FallbackWhenNoMarkers tests that a cell.go with zero markers
// triggers the yaml fallback.
func TestMerge_FallbackWhenNoMarkers(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	// Use the empty fixture as cell.go (no markers).
	copyFile(t, filepath.Join(td, "cell_empty.go"), filepath.Join(tmp, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"emptycell": {
				ID: "emptycell",
				Listeners: []metadata.ListenerDeclMeta{
					{Ref: "cell.InternalListener", Prefix: "/internal/v1"},
				},
			},
		},
		map[string]*metadata.SliceMeta{},
	)
	projectRoot := filepath.Dir(tmp)
	rel, _ := filepath.Rel(projectRoot, filepath.Join(tmp, "cell.yaml"))
	project.Cells["emptycell"].File = rel

	bundles, err := Merge(projectRoot, project)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	bundle := bundles["emptycell"]
	if len(bundle.Listeners) != 1 || bundle.Listeners[0].Ref != "cell.InternalListener" {
		t.Errorf("expected fallback listener, got %v", bundle.Listeners)
	}
}

// TestMerge_ErrorAccumulation tests that errors from multiple cells are
// aggregated and Merge returns a combined error.
func TestMerge_ErrorAccumulation(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	// Place a broken cell.go (missing ref).
	copyFile(t, filepath.Join(td, "cell_badfieldmarker.go"), filepath.Join(tmp, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"badcell": {},
		},
		map[string]*metadata.SliceMeta{},
	)
	projectRoot := filepath.Dir(tmp)
	rel, _ := filepath.Rel(projectRoot, filepath.Join(tmp, "cell.yaml"))
	project.Cells["badcell"].File = rel

	_, err := Merge(projectRoot, project)
	if err == nil {
		t.Fatal("expected error from bad marker")
	}
	if !strings.Contains(err.Error(), "ref") {
		t.Errorf("error should mention missing 'ref' field, got: %v", err)
	}
}

// TestMerge_EmptyProject tests that an empty ProjectMeta returns empty map and no error.
func TestMerge_EmptyProject(t *testing.T) {
	t.Parallel()
	project := buildProjectMeta(map[string]*metadata.CellMeta{}, map[string]*metadata.SliceMeta{})
	tmp := t.TempDir()
	bundles, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bundles) != 0 {
		t.Errorf("expected empty map, got %v", bundles)
	}
}

// copyFile copies src to dst (test helper).
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src) //nolint:gosec // test helper reads known testdata paths
	if err != nil {
		t.Fatalf("copyFile: read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0600); err != nil { //nolint:gosec // test helper writes to t.TempDir()
		t.Fatalf("copyFile: write %s: %v", dst, err)
	}
}
