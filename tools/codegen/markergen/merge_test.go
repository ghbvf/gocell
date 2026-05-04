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

	// Use a stable <tmp>/cells/markercell/ layout so Merge can compute
	// filepath.Join(projectRoot, "cells/markercell", "cell.go") deterministically.
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "markercell")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_withmarkers.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"markercell": {
				ID:   "markercell",
				File: "cells/markercell/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{
			"markercell/sliceA": {ID: "sliceA"},
			"markercell/sliceB": {ID: "sliceB"},
		},
	)

	// projectRoot = tmp; Merge will look for tmp/cells/markercell/cell.go.
	bundles, err := Merge(tmp, project)
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

// TestMerge_EmptyBundleWhenNoCellGo tests that when cell.go is absent,
// Merge returns an empty WireBundle (no fallback to yaml).
func TestMerge_EmptyBundleWhenNoCellGo(t *testing.T) {
	t.Parallel()
	// cell.File references a dir that has no cell.go sibling.
	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"nocellgo": {
				ID:   "nocellgo",
				File: "cells/nocellgo/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{},
	)

	// Use an empty temp dir as projectRoot — cells/nocellgo/cell.go won't exist.
	tmp := t.TempDir()
	bundles, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	bundle, ok := bundles["nocellgo"]
	if !ok {
		t.Fatal("bundle for nocellgo not found")
	}
	if len(bundle.Listeners) != 0 || len(bundle.Routes) != 0 || len(bundle.Subscribes) != 0 {
		t.Errorf("expected empty bundle when cell.go absent, got %+v", bundle)
	}
}

// TestMerge_EmptyBundleWhenNoMarkers tests that a cell.go with zero markers
// yields an empty WireBundle (no fallback to yaml).
func TestMerge_EmptyBundleWhenNoMarkers(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	// Use the empty fixture as cell.go (no markers).
	copyFile(t, filepath.Join(td, "cell_empty.go"), filepath.Join(tmp, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"emptycell": {
				ID: "emptycell",
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
	if len(bundle.Listeners) != 0 || len(bundle.Routes) != 0 || len(bundle.Subscribes) != 0 {
		t.Errorf("expected empty bundle when no markers, got %+v", bundle)
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
	if !strings.Contains(err.Error(), `missing required field "ref"`) {
		t.Errorf("error should contain `missing required field \"ref\"`, got: %v", err)
	}
	// SEC-03/OPS-04: error must carry cellID and must NOT contain absolute path.
	if !strings.Contains(err.Error(), "badcell") {
		t.Errorf("error should contain cellID 'badcell', got: %v", err)
	}
	if strings.HasPrefix(err.Error(), "/") || strings.Contains(err.Error(), projectRoot) {
		t.Errorf("error should use relative path, not absolute: %v", err)
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

// TestMerge_GhostSliceRoute tests that a route marker referencing a slice not
// present in ProjectMeta.Slices produces an "unknown slice" error.
func TestMerge_GhostSliceRoute(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "ghostcell")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_ghostslice.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"ghostcell": {
				ID:   "ghostcell",
				File: "cells/ghostcell/cell.yaml",
			},
		},
		// Only "real" is declared; "ghost" and "phantomslice" are missing.
		map[string]*metadata.SliceMeta{
			"ghostcell/real": {ID: "real"},
		},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for unknown slice reference, got nil")
	}
	if !strings.Contains(err.Error(), `unknown slice "ghost"`) {
		t.Errorf("error should contain 'unknown slice \"ghost\"', got: %v", err)
	}
	if !strings.Contains(err.Error(), `unknown slice "phantomslice"`) {
		t.Errorf("error should contain 'unknown slice \"phantomslice\"', got: %v", err)
	}
	if !strings.Contains(err.Error(), "declared slices:") {
		t.Errorf("error should list declared slices, got: %v", err)
	}
	if !strings.Contains(err.Error(), "real") {
		t.Errorf("error should mention the real slice name, got: %v", err)
	}
}

// TestMerge_SliceTypoFieldSuggestion tests that a typo "slcie=" produces an
// "unknown field" error with a Levenshtein "did you mean" suggestion.
func TestMerge_SliceTypoFieldSuggestion(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "typocell")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_slicetypo.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"typocell": {
				ID:   "typocell",
				File: "cells/typocell/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for typo field, got nil")
	}
	if !strings.Contains(err.Error(), `unknown field "slcie"`) {
		t.Errorf("error should contain 'unknown field \"slcie\"', got: %v", err)
	}
	if !strings.Contains(err.Error(), `did you mean "slice"`) {
		t.Errorf("error should suggest 'slice', got: %v", err)
	}
}

// TestMerge_ValidSliceOwnership tests that when all marker slice references
// exist in ProjectMeta.Slices, Merge succeeds without error.
func TestMerge_ValidSliceOwnership(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "markercell2")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_withmarkers.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"markercell2": {
				ID:   "markercell2",
				File: "cells/markercell2/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{
			"markercell2/sliceA": {ID: "sliceA"},
			"markercell2/sliceB": {ID: "sliceB"},
		},
	)

	_, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("expected no error for valid slice ownership, got: %v", err)
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
