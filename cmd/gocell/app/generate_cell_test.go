package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateCell_DryRunVerifyMutex(t *testing.T) {
	t.Parallel()
	err := generateCell([]string{"--dry-run", "--verify", "demo"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

// NB: TestGenerateCell_NoArgs / _AllAndPositionalMutex deleted in K#05 W2 —
// `--all` defaults to true (no-args is now equivalent to --all), and
// positional ids beat the default flag (no mutex error). Coverage moved
// to codegen_cmd_test.go's table-driven cases.

func TestGenerateCell_UnknownFlag(t *testing.T) {
	t.Parallel()
	// flag.ContinueOnError makes Parse return an error for unknown flags
	// (it also writes to its Output, which we don't capture here).
	err := generateCell([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("expected error from flag parser")
	}
}

// TestGenerateCell_SuccessPath creates a minimal fake project in a temp dir,
// points findRoot at it via os.Chdir, and invokes generateCell(["demo"]).
// It asserts that the command returns nil and that cell_gen.go is written.
//
// findRoot walks up from cwd looking for go.mod, so os.Chdir into the temp
// root is the cleanest way to redirect it without changing the function
// signature. t.Cleanup restores cwd after the test.
func TestGenerateCell_SuccessPath(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root := t.TempDir()

	// Minimal go.mod so findRoot resolves to root.
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/testproject\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// cells/demo/cell.yaml — goStructName opts this cell into codegen.
	cellDir := filepath.Join(root, "cells", "demo")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir cell: %v", err)
	}
	cellYAML := "id: demo\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: testteam\n  role: owner\n" +
		"schema:\n  primary: demo_table\n" +
		"verify:\n  smoke: []\ngoStructName: Demo\n"
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatalf("write cell.yaml: %v", err)
	}

	// cells/demo/slices/alpha/slice.yaml — minimal slice with no subscribes
	// so slice_gen.go is not produced (sliceSpec == nil path in generator).
	sliceDir := filepath.Join(cellDir, "slices", "alpha")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir slice: %v", err)
	}
	sliceYAML := "id: alpha\nbelongsToCell: demo\nconsistencyLevel: L1\ncontractUsages: []\n" +
		"verify:\n  unit: []\n  contract: []\n" +
		"allowedFiles:\n  - cells/demo/slices/alpha/**\n"
	if err := os.WriteFile(filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o644); err != nil {
		t.Fatalf("write slice.yaml: %v", err)
	}

	// Redirect findRoot to the temp project.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateCell([]string{"demo"}); err != nil {
		t.Fatalf("generateCell returned error: %v", err)
	}

	cellGenFile := filepath.Join(cellDir, "cell_gen.go")
	if _, err := os.Stat(cellGenFile); err != nil {
		t.Fatalf("expected cell_gen.go to be written at %s: %v", cellGenFile, err)
	}
}
