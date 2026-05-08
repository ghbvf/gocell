package markergen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
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
			"markercell/sliceA": {
				ID: "sliceA",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "api.items.v1", Role: "serve"},
				},
			},
			"markercell/sliceB": {
				ID: "sliceB",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.foo.v1", Role: "subscribe"},
				},
			},
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
			"markercell2/sliceA": {
				ID: "sliceA",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "api.items.v1", Role: "serve"},
				},
			},
			"markercell2/sliceB": {
				ID: "sliceB",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.foo.v1", Role: "subscribe"},
				},
			},
		},
	)

	_, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("expected no error for valid slice ownership, got: %v", err)
	}
}

// TestMerge_ListenerOnField tests that placing a cell:listener marker on a
// struct field (instead of the type declaration) produces an error (K05-04).
func TestMerge_ListenerOnField(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "badlistener")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_listener_on_field.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"badlistener": {
				ID:   "badlistener",
				File: "cells/badlistener/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for cell:listener on field, got nil")
	}
	if !strings.Contains(err.Error(), "cell:listener marker must be on a type declaration") {
		t.Errorf("error should mention type declaration, got: %v", err)
	}
	if !strings.Contains(err.Error(), "BadField") {
		t.Errorf("error should mention field name BadField, got: %v", err)
	}
}

// TestMerge_RouteOnType tests that placing a slice:route marker on a type
// declaration (instead of a named struct field) produces an error (K05-04).
func TestMerge_RouteOnType(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "routeontype")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_route_on_type.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"routeontype": {
				ID:   "routeontype",
				File: "cells/routeontype/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for slice:route on type declaration, got nil")
	}
	if !strings.Contains(err.Error(), "slice:route marker must be on a named struct field") {
		t.Errorf("error should mention named struct field, got: %v", err)
	}
}

// TestMerge_ContractUsageRoleServe tests that a route marker slice missing
// role=serve in contractUsages produces an error (K05-01a).
func TestMerge_ContractUsageRoleServe(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "missingserve")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_missing_serve_role.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"missingserve": {
				ID:   "missingserve",
				File: "cells/missingserve/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{
			// sliceA exists but only has role=call, not role=serve.
			"missingserve/sliceA": {
				ID: "sliceA",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "api.items.v1", Role: "call"},
				},
			},
		},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for missing role=serve, got nil")
	}
	if !strings.Contains(err.Error(), `missing contractUsages role "serve"`) {
		t.Errorf("error should mention missing role serve, got: %v", err)
	}
	if !strings.Contains(err.Error(), "declared roles:") {
		t.Errorf("error should list declared roles, got: %v", err)
	}
	if !strings.Contains(err.Error(), "call") {
		t.Errorf("error should mention the declared role 'call', got: %v", err)
	}
}

// TestMerge_ContractUsageRoleSubscribe tests that a subscribe marker slice
// missing role=subscribe in contractUsages produces an error (K05-01a).
func TestMerge_ContractUsageRoleSubscribe(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "missingsubscribe")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_missing_subscribe_role.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"missingsubscribe": {
				ID:   "missingsubscribe",
				File: "cells/missingsubscribe/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{
			// sliceB exists but only has role=publish, not role=subscribe.
			"missingsubscribe/sliceB": {
				ID: "sliceB",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.foo.v1", Role: "publish"},
				},
			},
		},
	)

	_, err := Merge(tmp, project)
	if err == nil {
		t.Fatal("expected error for missing role=subscribe, got nil")
	}
	if !strings.Contains(err.Error(), `missing contractUsages role "subscribe"`) {
		t.Errorf("error should mention missing role subscribe, got: %v", err)
	}
	if !strings.Contains(err.Error(), "publish") {
		t.Errorf("error should mention the declared role 'publish', got: %v", err)
	}
}

// TestMerge_ContractUsageRoleValid tests that when slices correctly declare the
// required roles, Merge succeeds (K05-01a green path).
func TestMerge_ContractUsageRoleValid(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "validroles")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_withmarkers.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"validroles": {
				ID:   "validroles",
				File: "cells/validroles/cell.yaml",
			},
		},
		map[string]*metadata.SliceMeta{
			"validroles/sliceA": {
				ID: "sliceA",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "api.items.v1", Role: "serve"},
				},
			},
			"validroles/sliceB": {
				ID: "sliceB",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.foo.v1", Role: "subscribe"},
				},
			},
		},
	)

	_, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("expected no error for valid roles, got: %v", err)
	}
}

// TestMerge_ContractUsageRoleSkippedWhenNilMeta tests that when sliceMeta is
// nil (not yet in project), the role check is skipped (K05-01a nil guard).
func TestMerge_ContractUsageRoleSkippedWhenNilMeta(t *testing.T) {
	t.Parallel()
	td := testdataDir(t)
	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "nilmeta")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFile(t, filepath.Join(td, "cell_withmarkers.go"), filepath.Join(cellDir, "cell.go"))

	project := buildProjectMeta(
		map[string]*metadata.CellMeta{
			"nilmeta": {
				ID:   "nilmeta",
				File: "cells/nilmeta/cell.yaml",
			},
		},
		// Slices exist in the set but their metadata pointers are nil.
		map[string]*metadata.SliceMeta{
			"nilmeta/sliceA": nil,
			"nilmeta/sliceB": nil,
		},
	)

	// Should not panic; nil sliceMeta skips role check.
	_, err := Merge(tmp, project)
	if err != nil {
		t.Fatalf("expected no error when sliceMeta is nil, got: %v", err)
	}
}

// copyFile copies src to dst (test helper).
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data := fileutil.MustReadFile(t, src)
	fileutil.MustWriteFile(t, dst, data)
}
