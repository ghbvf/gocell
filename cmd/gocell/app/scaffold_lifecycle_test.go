package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestScaffoldCell_DefaultsLifecycleExperimental verifies that a scaffolded
// cell.yaml includes `lifecycle: experimental` as the default value.
//
// Rationale: cell.yaml schema requires a lifecycle field; "experimental" is
// the correct default for a newly scaffolded cell (not yet validated in
// production). This mirrors TestScaffoldJourney_DefaultsLifecycleExperimental
// for the journey path.
func TestScaffoldCell_DefaultsLifecycleExperimental(t *testing.T) {
	t.Parallel()

	root := setupProject(t, "cells")

	if err := runScaffoldWithRoot(context.Background(), root, []string{
		"cell",
		"--id=lifecyclecell",
		"--team=platform",
		"--role=cell-owner",
	}); err != nil {
		t.Fatalf("scaffold cell: %v", err)
	}

	cellYAMLFile := filepath.Join(root, "cells", "lifecyclecell", "cell.yaml")
	data, err := os.ReadFile(cellYAMLFile) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read cell.yaml: %v", err)
	}

	// Structural check: must contain `lifecycle:` key.
	if !strings.Contains(string(data), "lifecycle:") {
		t.Errorf("cell.yaml must contain 'lifecycle:' field\ncontent:\n%s", data)
	}

	// YAML round-trip check: lifecycle value must be "experimental".
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("cell.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	lc, ok := parsed["lifecycle"]
	if !ok {
		t.Fatalf("cell.yaml missing 'lifecycle' field\ncontent:\n%s", data)
	}
	if lc != "experimental" {
		t.Errorf("lifecycle = %q, want %q\ncontent:\n%s", lc, "experimental", data)
	}
}

// TestScaffoldSlice_DefaultsLifecycleExperimental verifies that a scaffolded
// slice.yaml includes `lifecycle: experimental` as the default value.
//
// Rationale: slice.yaml schema requires a lifecycle field; "experimental" is
// the correct default for a newly scaffolded slice. This mirrors
// TestScaffoldJourney_DefaultsLifecycleExperimental for the slice path.
func TestScaffoldSlice_DefaultsLifecycleExperimental(t *testing.T) {
	t.Parallel()

	root := setupProject(t, "cells")

	// First create the parent cell so scaffold slice can validate it exists.
	if err := runScaffoldWithRoot(context.Background(), root, []string{
		"cell",
		"--id=parentcell",
		"--team=platform",
		"--role=cell-owner",
	}); err != nil {
		t.Fatalf("scaffold parent cell: %v", err)
	}

	if err := runScaffoldWithRoot(context.Background(), root, []string{
		"slice",
		"--id=lifecycleslice",
		"--cell=parentcell",
	}); err != nil {
		t.Fatalf("scaffold slice: %v", err)
	}

	sliceYAMLFile := filepath.Join(root, "cells", "parentcell", "slices", "lifecycleslice", "slice.yaml")
	data, err := os.ReadFile(sliceYAMLFile) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read slice.yaml: %v", err)
	}

	// Structural check: must contain `lifecycle:` key.
	if !strings.Contains(string(data), "lifecycle:") {
		t.Errorf("slice.yaml must contain 'lifecycle:' field\ncontent:\n%s", data)
	}

	// YAML round-trip check: lifecycle value must be "experimental".
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("slice.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	lc, ok := parsed["lifecycle"]
	if !ok {
		t.Fatalf("slice.yaml missing 'lifecycle' field\ncontent:\n%s", data)
	}
	if lc != "experimental" {
		t.Errorf("lifecycle = %q, want %q\ncontent:\n%s", lc, "experimental", data)
	}
}
