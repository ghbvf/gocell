package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunScaffoldAssembly_Basic is a RED test for K#09 `gocell scaffold assembly`:
// flag parsing, cell existence check, output file inventory, default --deploy=k8s
// (no deployTemplate written).
func TestRunScaffoldAssembly_Basic(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=myassembly",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly: %v", err)
	}

	wants := []string{
		"assemblies/myassembly/assembly.yaml",
		"cmd/myassembly/run.go",
		"cmd/myassembly/app.go",
	}
	for _, rel := range wants {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("scaffold missing %s: %v", rel, err)
		}
	}

	asmYAML, _ := os.ReadFile(filepath.Join(root, "assemblies", "myassembly", "assembly.yaml")) //nolint:gosec // tempdir test fixture
	if strings.Contains(string(asmYAML), "deployTemplate") {
		t.Errorf("--deploy default (k8s) must not write deployTemplate; got:\n%s", asmYAML)
	}
}

// TestRunScaffoldAssembly_UnknownCell rejects --cells referencing unknown cell.
func TestRunScaffoldAssembly_UnknownCell(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=myassembly",
		"--cells=examplecell,doesnotexist",
		"--team=platform",
		"--role=maintainer",
	}
	err := scaffoldAssembly(root, args)
	if err == nil {
		t.Fatal("expected error for unknown cell, got nil")
	}
	if !strings.Contains(err.Error(), "doesnotexist") {
		t.Errorf("error must name the missing cell; got: %v", err)
	}
}

// TestRunScaffoldAssembly_DryRun produces no files.
func TestRunScaffoldAssembly_DryRun(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=dryasm",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
		"--dry-run",
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assemblies", "dryasm")); err == nil {
		t.Errorf("dry-run wrote files to disk")
	}
}

// TestRunScaffoldAssembly_SkipGenerate verifies that --skip-generate writes the
// scaffold skeleton (assembly.yaml + run.go) but does NOT write the auto-generate
// artifacts (modules_gen.go, boundary.yaml).
func TestRunScaffoldAssembly_SkipGenerate(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=siasm",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
		"--skip-generate",
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly --skip-generate: %v", err)
	}

	// Static files must exist.
	for _, rel := range []string{
		"assemblies/siasm/assembly.yaml",
		"cmd/siasm/run.go",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	// Auto-generated files must NOT exist.
	for _, rel := range []string{
		"cmd/siasm/modules_gen.go",
		"assemblies/siasm/generated/boundary.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("expected %s to be absent (--skip-generate), but it exists", rel)
		}
	}
}

// setupAssemblyTestProject creates a tempdir project with go.mod and the
// supplied cell skeleton (cell.yaml only — sufficient for assembly scaffold
// validation).
func setupAssemblyTestProject(t *testing.T, cellID string) string { //nolint:unparam // cellID kept as param for test readability
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cellDir := filepath.Join(root, "cells", cellID)
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cellYAML := `id: ` + cellID + `
type: core
consistencyLevel: L1
durabilityMode: durable
owner:
  team: platform
  role: cell-owner
schema:
  primary: ` + cellID + `
verify:
  smoke:
    - smoke.` + cellID + `.startup
goStructName: ExampleCell
l0Dependencies: []
`
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
