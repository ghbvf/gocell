package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunScaffoldCell_BundleProducesSliceAndContract is a RED test for K#09:
// `gocell scaffold cell` upgrade produces full bundle (cell + slice + contract).
//
// Replaces the old behavior where only cell.yaml + cell.go were emitted.
func TestRunScaffoldCell_BundleProducesSliceAndContract(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=mybundlecell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
		"--skip-generate", // RED scope: only verify scaffold output, not codegen invocation
	}
	if err := scaffoldCell(root, args); err != nil {
		t.Fatalf("scaffoldCell bundle: %v", err)
	}

	wants := []string{
		"cells/mybundlecell/cell.yaml",
		"cells/mybundlecell/cell.go",
		"cells/mybundlecell/slices/mybundlecellexample/slice.yaml",
		"cells/mybundlecell/slices/mybundlecellexample/service.go",
		"cells/mybundlecell/slices/mybundlecellexample/service_test.go",
		"contracts/http/mybundlecell/example/v1/contract.yaml",
		"contracts/http/mybundlecell/example/v1/request.schema.json",
		"contracts/http/mybundlecell/example/v1/response.schema.json",
	}
	for _, rel := range wants {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("bundle missing %s: %v", rel, err)
		}
	}

	// contract.yaml must NOT carry an explicit `codegen:` line (K#09 funnel default).
	cYAMLPath := filepath.Join(root, "contracts", "http", "mybundlecell", "example", "v1", "contract.yaml")
	c, _ := os.ReadFile(cYAMLPath) //nolint:gosec // tempdir test fixture
	if strings.Contains(string(c), "codegen:") {
		t.Errorf("scaffold contract.yaml must not declare codegen: (parser default true);\n got:\n%s", c)
	}
}

// TestRunScaffoldCell_BundleWithAutoGenerate covers the PlanCellBundleScaffold
// derived-codegen path: scaffold cell without --skip-generate must produce both
// cellgen and contractgen output (cell_gen.go + types_gen.go) so the bundle is
// buildable + testable end-to-end.
func TestRunScaffoldCell_BundleWithAutoGenerate(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=autogencell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
	}
	if err := scaffoldCell(root, args); err != nil {
		t.Fatalf("scaffoldCell auto-generate: %v", err)
	}

	wants := []string{
		"cells/autogencell/cell_gen.go",
		"generated/contracts/http/autogencell/example/v1/types_gen.go",
		"generated/contracts/http/autogencell/example/v1/iface_gen.go",
	}
	for _, rel := range wants {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("auto-generate missing %s: %v", rel, err)
		}
	}
}

func setupBundleTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy the shared error schema so contractgen (invoked by --skip-generate=false
	// paths) can resolve relative SchemaRef links in scaffolded contract.yaml files.
	schemaDir := filepath.Join(root, "contracts", "shared", "errors")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realSchemaPath := filepath.Join("..", "..", "..", "..", "contracts", "shared", "errors", "error-response-v1.schema.json")
	schemaContent, err := os.ReadFile(realSchemaPath) //nolint:gosec // test fixture from known path
	if err != nil {
		t.Skipf("cannot read shared error schema (not in expected location): %v", err)
	}
	schemaOut := filepath.Join(schemaDir, "error-response-v1.schema.json")
	if err := os.WriteFile(schemaOut, schemaContent, 0o644); err != nil { //nolint:gosec // tempdir test fixture
		t.Fatal(err)
	}

	return root
}

// ---------------------------------------------------------------------------
// D7 RED tests — SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01
// ---------------------------------------------------------------------------

// TestRunScaffoldCell_DryRun_PrintsDerivedPaths verifies that --dry-run prints
// BOTH skeleton paths AND derived codegen paths (cell_gen.go, types_gen.go,
// iface_gen.go, handler_gen.go), and writes ZERO files to disk.
//
// GREEN: PlanCellBundleScaffold (via appendDerivedCodegenStaged) merges skeleton
// and derived files into a single plan; dry-run prints the full merged plan.
func TestRunScaffoldCell_DryRun_PrintsDerivedPaths(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=drycell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
		"--dry-run",
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = scaffoldCell(root, args)
	})
	if runErr != nil {
		t.Fatalf("scaffoldCell dry-run: %v", runErr)
	}

	// Skeleton paths must appear.
	skeletonPaths := []string{
		"cells/drycell/cell.yaml",
		"cells/drycell/cell.go",
		"cells/drycell/slices/drycellexample/slice.yaml",
	}
	for _, rel := range skeletonPaths {
		wantLine := fmt.Sprintf("(dry-run) Would create %s", rel)
		if !strings.Contains(out, wantLine) {
			t.Errorf("dry-run output missing skeleton path %q\nfull output:\n%s", wantLine, out)
		}
	}

	// Derived codegen paths must also appear — fatal so test doesn't silently
	// pass vacuously when contractgen skips rendering.
	derivedPaths := []string{
		"cells/drycell/cell_gen.go",
		"generated/contracts/http/drycell/example/v1/types_gen.go",
		"generated/contracts/http/drycell/example/v1/iface_gen.go",
		"generated/contracts/http/drycell/example/v1/handler_gen.go",
	}
	for _, rel := range derivedPaths {
		wantLine := fmt.Sprintf("(dry-run) Would create %s", rel)
		if !strings.Contains(out, wantLine) {
			t.Fatalf("dry-run output missing derived path %q\nfull output:\n%s", wantLine, out)
		}
	}

	// ZERO files written to disk — including no leftover staging dir.
	if _, err := os.Stat(filepath.Join(root, "cells", "drycell")); err == nil {
		t.Error("dry-run: cells/drycell must not exist on disk")
	}
	if _, err := os.Stat(filepath.Join(root, "generated")); err == nil {
		t.Error("dry-run: generated/ must not exist on disk")
	}
}

// TestRunScaffoldCell_LiveRollback_OnDerivedConflict verifies that when a derived
// path pre-exists as a conflicting non-overwritable obstacle, the ENTIRE plan
// (skeleton + derived) rolls back: both skeleton files and derived files are absent.
//
// RED: current code writes skeleton first then derived in a separate call;
// derived failure leaves skeleton half-written.
func TestRunScaffoldCell_LiveRollback_OnDerivedConflict(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	// Pre-place a non-ForceOverwrite conflicting directory at the types_gen.go path
	// that will cause WritePlannedFiles to fail on the derived slot.
	conflictDir := filepath.Join(root, "generated", "contracts", "http", "rollbackcell", "example", "v1", "types_gen.go")
	if err := os.MkdirAll(conflictDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Now types_gen.go is a directory — writing a file at that path will fail.

	args := []string{
		"--id=rollbackcell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
	}
	err := scaffoldCell(root, args)
	if err == nil {
		t.Fatal("scaffoldCell with conflicting derived path: want error, got nil")
	}

	// Skeleton files must not exist after rollback.
	for _, rel := range []string{
		"cells/rollbackcell/cell.yaml",
		"cells/rollbackcell/cell.go",
		"cells/rollbackcell/slices/rollbackcellexample/slice.yaml",
	} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("rollback: file must not exist: %s", rel)
		}
	}
	// Derived files must not exist after rollback (except the pre-placed obstacle).
	for _, rel := range []string{
		"cells/rollbackcell/cell_gen.go",
	} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("rollback: derived file must not exist: %s", rel)
		}
	}
}

// TestRunScaffoldCell_SkipGenerate_NoDerived verifies that --skip-generate
// produces only skeleton files and no derived codegen (*_gen.go) files.
//
// This tests the new SkipGenerate path in PlanCellBundleScaffold.
func TestRunScaffoldCell_SkipGenerate_NoDerived(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=skipgencell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
		"--skip-generate",
	}
	if err := scaffoldCell(root, args); err != nil {
		t.Fatalf("scaffoldCell --skip-generate: %v", err)
	}

	// Skeleton files must exist.
	for _, rel := range []string{
		"cells/skipgencell/cell.yaml",
		"cells/skipgencell/cell.go",
		"cells/skipgencell/slices/skipgencellexample/slice.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("skip-generate: skeleton file missing %s: %v", rel, err)
		}
	}

	// Derived codegen files must NOT exist.
	for _, rel := range []string{
		"cells/skipgencell/cell_gen.go",
		"generated/contracts/http/skipgencell/example/v1/types_gen.go",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("skip-generate: derived file must not exist: %s", rel)
		}
	}
}
