package cellgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScaffoldCellBundle_Projection asserts that --projection produces:
//   - projection slice.yaml (with projection: field + subscribe + provide CUs, L3)
//   - kind:projection contract.yaml (ownerCell, L3, replayable)
//   - payload.schema.json for the projection contract
//   - event contract.yaml (so the subscribe CU target exists)
//   - internal/projection/doc.go starter
//
// When ONLY --projection is set, no HTTP contract is scaffolded (the default
// HTTP gate must also require !WithProjection).
func TestScaffoldCellBundle_Projection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           mustID(t, "myprojcell"),
		StructName:       "MyProjCell",
		Package:          "myprojcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L3",
		WithProjection:   true,
	}

	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip(--projection): %v", err)
	}

	// --- File inventory ---

	// Projection slice files
	wantFiles := []string{
		"cells/myprojcell/cell.yaml",
		"cells/myprojcell/cell.go",
		"cells/myprojcell/internal/ports/doc.go",
		"cells/myprojcell/internal/mem/doc.go",
		// projection-specific internal layer
		"cells/myprojcell/internal/projection/doc.go",
		// projection slice
		"cells/myprojcell/slices/myprojcellprojection/slice.yaml",
		"cells/myprojcell/slices/myprojcellprojection/service.go",
		"cells/myprojcell/slices/myprojcellprojection/service_test.go",
		// projection contract
		"contracts/projection/myprojcell/summary/v1/contract.yaml",
		"contracts/projection/myprojcell/summary/v1/payload.schema.json",
		// event contract (subscribe target)
		"contracts/event/myprojcell/example/v1/contract.yaml",
		"contracts/event/myprojcell/example/v1/payload.schema.json",
		"contracts/event/myprojcell/example/v1/headers.schema.json",
	}
	for _, rel := range wantFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("projection bundle missing %s: %v", rel, err)
		}
	}

	// --- No HTTP contract when only --projection set ---
	httpContract := filepath.Join(dir, "contracts", "http", "myprojcell", "example", "v1", "contract.yaml")
	if _, err := os.Stat(httpContract); err == nil {
		t.Errorf("--projection-only must NOT produce an HTTP contract; found %s", httpContract)
	}

	// --- projection slice.yaml correctness ---
	projSliceYAML, err := os.ReadFile( //nolint:gosec // tempdir test fixture
		filepath.Join(dir, "cells", "myprojcell", "slices", "myprojcellprojection", "slice.yaml"))
	if err != nil {
		t.Fatalf("read projection slice.yaml: %v", err)
	}
	projSlice := string(projSliceYAML)

	// consistencyLevel must be L3
	if !strings.Contains(projSlice, "consistencyLevel: L3") {
		t.Errorf("projection slice.yaml must declare consistencyLevel: L3; got:\n%s", projSlice)
	}
	// subscribe CU with projection: field
	if !strings.Contains(projSlice, "role: subscribe") {
		t.Errorf("projection slice.yaml must have subscribe CU; got:\n%s", projSlice)
	}
	if !strings.Contains(projSlice, "projection:") {
		t.Errorf("projection slice.yaml subscribe CU must carry projection: field; got:\n%s", projSlice)
	}
	// provide CU for the projection contract
	if !strings.Contains(projSlice, "role: provide") {
		t.Errorf("projection slice.yaml must have provide CU; got:\n%s", projSlice)
	}
	if !strings.Contains(projSlice, "projection.myprojcell.summary.v1") {
		t.Errorf("projection slice.yaml provide CU must reference the projection contract id; got:\n%s", projSlice)
	}

	// --- projection contract.yaml correctness ---
	projContractYAML, err := os.ReadFile( //nolint:gosec // tempdir test fixture
		filepath.Join(dir, "contracts", "projection", "myprojcell", "summary", "v1", "contract.yaml"))
	if err != nil {
		t.Fatalf("read projection contract.yaml: %v", err)
	}
	projContract := string(projContractYAML)

	if !strings.Contains(projContract, "kind: projection") {
		t.Errorf("projection contract.yaml must declare kind: projection; got:\n%s", projContract)
	}
	if !strings.Contains(projContract, "consistencyLevel: L3") {
		t.Errorf("projection contract.yaml must declare consistencyLevel: L3; got:\n%s", projContract)
	}
	if !strings.Contains(projContract, "replayable: true") {
		t.Errorf("projection contract.yaml must declare replayable: true; got:\n%s", projContract)
	}
	if !strings.Contains(projContract, "ownerCell: myprojcell") {
		t.Errorf("projection contract.yaml must set ownerCell; got:\n%s", projContract)
	}

	// --- internal/projection/doc.go ---
	docGo, err := os.ReadFile( //nolint:gosec // tempdir test fixture
		filepath.Join(dir, "cells", "myprojcell", "internal", "projection", "doc.go"))
	if err != nil {
		t.Fatalf("read internal/projection/doc.go: %v", err)
	}
	docGoStr := string(docGo)
	if !strings.HasPrefix(docGoStr, "// Package projection ") {
		t.Errorf("internal/projection/doc.go must start with `// Package projection `; got:\n%s", docGoStr)
	}
	if !strings.Contains(docGoStr, "\npackage projection\n") {
		t.Errorf("internal/projection/doc.go must declare `package projection`; got:\n%s", docGoStr)
	}

	// --- projection contract must NOT carry codegen: field (K#09 funnel) ---
	if strings.Contains(projContract, "codegen:") {
		t.Errorf("projection contract.yaml must not declare codegen field; got:\n%s", projContract)
	}
}

// TestScaffoldCellBundle_ProjectionDefaultGate asserts that when neither
// WithHTTP, WithEvents, WithBoth, nor WithProjection is set, the default is
// still HTTP (not projection).
func TestScaffoldCellBundle_ProjectionDefaultGate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           mustID(t, "defprojcell"),
		StructName:       "DefProjCell",
		Package:          "defprojcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		// No flag set → default HTTP
	}
	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip default: %v", err)
	}
	// Default HTTP contract must exist.
	if _, err := os.Stat(filepath.Join(dir, "contracts", "http", "defprojcell", "example", "v1", "contract.yaml")); err != nil {
		t.Errorf("default (no flags) must produce HTTP contract; got: %v", err)
	}
	// No projection contract.
	if _, err := os.Stat(filepath.Join(dir, "contracts", "projection")); err == nil {
		t.Errorf("default (no flags) must NOT produce a projection contract")
	}
	// No internal/projection dir.
	if _, err := os.Stat(filepath.Join(dir, "cells", "defprojcell", "internal", "projection")); err == nil {
		t.Errorf("default (no flags) must NOT create internal/projection")
	}
}

// TestScaffoldCellBundle_ProjectionAndHTTP asserts that --projection combined
// with --with-http produces both the HTTP slice AND the projection slice without
// path collisions. The projection slice gets a distinct sliceID.
func TestScaffoldCellBundle_ProjectionAndHTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           mustID(t, "bothprojcell"),
		StructName:       "BothProjCell",
		Package:          "bothprojcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L3",
		WithHTTP:         true,
		WithProjection:   true,
	}
	if err := scaffoldBundleSkip(t, dir, spec); err != nil {
		t.Fatalf("scaffoldBundleSkip(--with-http --projection): %v", err)
	}

	// HTTP slice must exist.
	if _, err := os.Stat(filepath.Join(dir, "cells", "bothprojcell", "slices", "bothprojcellexample", "slice.yaml")); err != nil {
		t.Errorf("--with-http must produce HTTP slice; got: %v", err)
	}
	// Projection slice must exist with distinct ID.
	if _, err := os.Stat(filepath.Join(dir, "cells", "bothprojcell", "slices", "bothprojcellprojection", "slice.yaml")); err != nil {
		t.Errorf("--projection must produce projection slice; got: %v", err)
	}
	// Projection contract must exist.
	if _, err := os.Stat(filepath.Join(dir, "contracts", "projection", "bothprojcell", "summary", "v1", "contract.yaml")); err != nil {
		t.Errorf("--projection must produce projection contract; got: %v", err)
	}
	// internal/projection/doc.go must exist.
	if _, err := os.Stat(filepath.Join(dir, "cells", "bothprojcell", "internal", "projection", "doc.go")); err != nil {
		t.Errorf("--projection must produce internal/projection/doc.go; got: %v", err)
	}
}
