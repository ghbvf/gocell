package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestGenerator_Scaffold is a RED test for K#09 kernel/assembly.Generator.Scaffold:
// produces assembly.yaml + cmd/{id}/run.go + cmd/{id}/app.go in one shot,
// then auto-invokes GenerateModulesGen / GenerateEntrypoint / GenerateBoundary.
func TestGenerator_Scaffold(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	// Pre-populate one cell so --cells reference is valid.
	cellDir := filepath.Join(root, "cells", "examplecell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cellYAML := `id: examplecell
type: core
consistencyLevel: L1
durabilityMode: durable
owner:
  team: platform
  role: cell-owner
schema:
  primary: examplecell
verify:
  smoke:
    - smoke.examplecell.startup
goStructName: ExampleCell
l0Dependencies: []
`
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Place a go.mod so module path discovery works.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Parse the project to feed into Generator.
	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.Parse: %v", err)
	}

	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID:        "myassembly",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
		Deploy:    "k8s", // default → omit deployTemplate from yaml
	}
	if err := gen.Scaffold(spec); err != nil {
		t.Fatalf("Generator.Scaffold: %v", err)
	}

	// Verify file inventory.
	wantFiles := []string{
		"assemblies/myassembly/assembly.yaml",
		"cmd/myassembly/run.go",
		"cmd/myassembly/app.go",
	}
	for _, rel := range wantFiles {
		full := filepath.Join(root, rel)
		if _, statErr := os.Stat(full); statErr != nil {
			t.Errorf("scaffold missing %s: %v", rel, statErr)
		}
	}

	// assembly.yaml minimal form: --deploy=k8s (default) → omit deployTemplate.
	asmYAML, err := os.ReadFile(filepath.Join(root, "assemblies/myassembly/assembly.yaml"))
	if err != nil {
		t.Fatalf("read assembly.yaml: %v", err)
	}
	got := string(asmYAML)
	if strings.Contains(got, "deployTemplate") {
		t.Errorf("--deploy=k8s default should omit deployTemplate; got:\n%s", got)
	}
	for _, want := range []string{"id: myassembly", "examplecell", "platform", "maintainer"} {
		if !strings.Contains(got, want) {
			t.Errorf("assembly.yaml missing %q; got:\n%s", want, got)
		}
	}
}

// TestGenerator_Scaffold_DeployCompose verifies non-k8s deploy writes deployTemplate.
func TestGenerator_Scaffold_DeployCompose(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cellDir := filepath.Join(root, "cells", "examplecell")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cellYAML := `id: examplecell
type: core
consistencyLevel: L1
durabilityMode: durable
owner:
  team: platform
  role: cell-owner
schema:
  primary: examplecell
verify:
  smoke:
    - smoke.examplecell.startup
goStructName: ExampleCell
l0Dependencies: []
`
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.Parse: %v", err)
	}
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID:        "myassembly",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
		Deploy:    "compose",
	}
	if err := gen.Scaffold(spec); err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	asmYAML, _ := os.ReadFile(filepath.Join(root, "assemblies/myassembly/assembly.yaml"))
	got := string(asmYAML)
	if !strings.Contains(got, "deployTemplate: compose") {
		t.Errorf("--deploy=compose should write deployTemplate; got:\n%s", got)
	}
}
