package cellgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScaffoldCellBundle_HTTP is a RED test for K#09 cellgen.ScaffoldCellBundle:
// produces cell + 1 example slice + 1 HTTP contract bundle in one shot.
//
// Bundle output (relative to root):
//
//	cells/{id}/cell.yaml
//	cells/{id}/cell.go
//	cells/{id}/slices/{id}example/slice.yaml
//	cells/{id}/slices/{id}example/service.go
//	cells/{id}/slices/{id}example/service_test.go
//	contracts/http/{id}/example/v1/contract.yaml
//	contracts/http/{id}/example/v1/request.schema.json
//	contracts/http/{id}/example/v1/response.schema.json
func TestScaffoldCellBundle_HTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "myhttpcell",
		StructName:       "MyHTTPCell",
		Package:          "myhttpcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}

	// Verify bundle file inventory.
	wantFiles := []string{
		"cells/myhttpcell/cell.yaml",
		"cells/myhttpcell/cell.go",
		"cells/myhttpcell/slices/myhttpcellexample/slice.yaml",
		"cells/myhttpcell/slices/myhttpcellexample/service.go",
		"cells/myhttpcell/slices/myhttpcellexample/service_test.go",
		"contracts/http/myhttpcell/example/v1/contract.yaml",
		"contracts/http/myhttpcell/example/v1/request.schema.json",
		"contracts/http/myhttpcell/example/v1/response.schema.json",
	}
	for _, rel := range wantFiles {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("bundle missing %s: %v", rel, err)
		}
	}

	// Verify contract.yaml does NOT carry an explicit `codegen:` line —
	// K#09 funnel: parser defaults Codegen to true so the field is redundant.
	// INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL
	cYAMLPath := filepath.Join(dir, "contracts", "http", "myhttpcell", "example", "v1", "contract.yaml")
	contractYAML, err := os.ReadFile(cYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read contract.yaml: %v", err)
	}
	if strings.Contains(string(contractYAML), "codegen:") {
		t.Errorf("scaffold contract.yaml must not declare codegen field (parser defaults to true); got:\n%s",
			string(contractYAML))
	}
}

// TestScaffoldCellBundle_Events is a RED test for the --with-events variant:
// produces an event contract with payload+headers schemas.
func TestScaffoldCellBundle_Events(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "myevtcell",
		StructName:       "MyEvtCell",
		Package:          "myevtcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithEvents:       true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}

	wantFiles := []string{
		"cells/myevtcell/cell.yaml",
		"cells/myevtcell/cell.go",
		"cells/myevtcell/slices/myevtcellexample/slice.yaml",
		"cells/myevtcell/slices/myevtcellexample/service.go",
		"cells/myevtcell/slices/myevtcellexample/service_test.go",
		"contracts/event/myevtcell/example/v1/contract.yaml",
		"contracts/event/myevtcell/example/v1/payload.schema.json",
		"contracts/event/myevtcell/example/v1/headers.schema.json",
	}
	for _, rel := range wantFiles {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("event bundle missing %s: %v", rel, err)
		}
	}
}

// TestScaffoldCellBundle_DryRun verifies dry-run produces no files.
func TestScaffoldCellBundle_DryRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "drycell",
		StructName:       "DryCell",
		Package:          "drycell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
		DryRun:           true,
	}
	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("dry-run ScaffoldCellBundle: %v", err)
	}
	// In dry-run, the cell directory must not exist.
	if _, err := os.Stat(filepath.Join(dir, "cells", "drycell")); err == nil {
		t.Errorf("dry-run scaffold wrote files to disk")
	}
}

// TestScaffoldCellBundle_WithBoth verifies that --with-both produces both an HTTP
// slice (sliceID={id}example) and a separate event slice (sliceID={id}eventexample),
// each with their own contractUsages entry, so gocell validate ADV-06 passes.
func TestScaffoldCellBundle_WithBoth(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "mybothcell",
		StructName:       "MyBothCell",
		Package:          "mybothcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithBoth:         true,
	}

	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle WithBoth: %v", err)
	}

	// HTTP slice and contract must exist.
	httpFiles := []string{
		"cells/mybothcell/slices/mybothcellexample/slice.yaml",
		"cells/mybothcell/slices/mybothcellexample/service.go",
		"cells/mybothcell/slices/mybothcellexample/service_test.go",
		"contracts/http/mybothcell/example/v1/contract.yaml",
		"contracts/http/mybothcell/example/v1/request.schema.json",
		"contracts/http/mybothcell/example/v1/response.schema.json",
	}
	for _, rel := range httpFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("WithBoth: missing HTTP file %s: %v", rel, err)
		}
	}

	// Event slice (separate sliceID) and contract must also exist.
	eventFiles := []string{
		"cells/mybothcell/slices/mybothcelleventexample/slice.yaml",
		"cells/mybothcell/slices/mybothcelleventexample/service.go",
		"cells/mybothcell/slices/mybothcelleventexample/service_test.go",
		"contracts/event/mybothcell/example/v1/contract.yaml",
		"contracts/event/mybothcell/example/v1/payload.schema.json",
		"contracts/event/mybothcell/example/v1/headers.schema.json",
	}
	for _, rel := range eventFiles {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("WithBoth: missing event file %s: %v", rel, err)
		}
	}

	// HTTP slice.yaml must reference the HTTP contract.
	httpSliceYAMLPath := filepath.Join(dir, "cells", "mybothcell", "slices", "mybothcellexample", "slice.yaml")
	httpSliceYAML, err := os.ReadFile(httpSliceYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read HTTP slice.yaml: %v", err)
	}
	if !strings.Contains(string(httpSliceYAML), "http.mybothcell.example.v1") {
		t.Errorf("HTTP slice.yaml must reference http.mybothcell.example.v1; got:\n%s", httpSliceYAML)
	}

	// Event slice.yaml must reference the event contract.
	evtSliceYAMLPath := filepath.Join(dir, "cells", "mybothcell", "slices", "mybothcelleventexample", "slice.yaml")
	evtSliceYAML, err := os.ReadFile(evtSliceYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read event slice.yaml: %v", err)
	}
	if !strings.Contains(string(evtSliceYAML), "event.mybothcell.example.v1") {
		t.Errorf("event slice.yaml must reference event.mybothcell.example.v1; got:\n%s", evtSliceYAML)
	}
}

// TestScaffoldCellBundle_BundleDefaultIsHTTP verifies that when neither
// WithHTTP nor WithEvents is set, default is HTTP.
func TestScaffoldCellBundle_BundleDefaultIsHTTP(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:           "defcell",
		StructName:       "DefCell",
		Package:          "defcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}
	if err := ScaffoldCellBundle(dir, spec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "contracts", "http", "defcell", "example", "v1", "contract.yaml")); err != nil {
		t.Errorf("default bundle should produce HTTP contract; got: %v", err)
	}
}
