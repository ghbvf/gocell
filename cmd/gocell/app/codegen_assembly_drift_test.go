package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

// minimalAssemblyProject creates a fake project root with:
//   - go.mod
//   - cells/alpha/cell.yaml  (goStructName: Alpha)
//   - cells/alpha/slices/s1/slice.yaml
//   - assemblies/myasm/assembly.yaml  (cells: [alpha])
//
// It also pre-generates cmd/myasm/modules_gen.go so the project starts in a
// "clean" state.
func minimalAssemblyProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/asmtest\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// cell
	cellDir := filepath.Join(root, "cells", "alpha")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir cell: %v", err)
	}
	cellYAML := "id: alpha\ntype: core\nconsistencyLevel: L1\n" +
		"owner:\n  team: testteam\n  role: owner\n" +
		"schema:\n  primary: alpha_table\n" +
		"verify:\n  smoke: []\ngoStructName: Alpha\n"
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
		t.Fatalf("write cell.yaml: %v", err)
	}

	sliceDir := filepath.Join(cellDir, "slices", "s1")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir slice: %v", err)
	}
	sliceYAML := "id: s1\nbelongsToCell: alpha\nconsistencyLevel: L1\ncontractUsages: []\n" +
		"verify:\n  unit: []\n  contract: []\n" +
		"allowedFiles:\n  - cells/alpha/slices/s1/**\n"
	if err := os.WriteFile(filepath.Join(sliceDir, "slice.yaml"), []byte(sliceYAML), 0o644); err != nil {
		t.Fatalf("write slice.yaml: %v", err)
	}

	// assembly
	asmDir := filepath.Join(root, "assemblies", "myasm")
	if err := os.MkdirAll(asmDir, 0o755); err != nil {
		t.Fatalf("mkdir assembly: %v", err)
	}
	asmYAML := "id: myasm\ncells:\n  - alpha\n" +
		"owner:\n  team: testteam\n" +
		"build:\n  entrypoint: cmd/myasm/main.go\n"
	if err := os.WriteFile(filepath.Join(asmDir, "assembly.yaml"), []byte(asmYAML), 0o644); err != nil {
		t.Fatalf("write assembly.yaml: %v", err)
	}

	// Pre-generate modules_gen.go so the project starts clean.
	preRenderAssemblyModulesGen(t, root)
	return root
}

// preRenderAssemblyModulesGen generates all assembly modules_gen.go files for
// the project at root using the generateAssemblyModulesGen helper (verify=false).
func preRenderAssemblyModulesGen(t *testing.T, root string) {
	t.Helper()
	project, err := parseProject(root)
	if err != nil {
		t.Fatalf("pre-render metadata parse: %v", err)
	}
	if _, err := generateAssemblyModulesGen(root, project, false, false, ""); err != nil {
		t.Fatalf("pre-render generateAssemblyModulesGen: %v", err)
	}
}

// TestCollectAssemblyModulesGenDrift_NoDrift verifies that a freshly generated
// project produces no drift.
func TestCollectAssemblyModulesGenDrift_NoDrift(t *testing.T) {
	root := minimalAssemblyProject(t)
	project, err := parseProject(root)
	if err != nil {
		t.Fatalf("parseProject: %v", err)
	}
	res, err := generateAssemblyModulesGen(root, project, false, true, "")
	if err != nil {
		t.Fatalf("generateAssemblyModulesGen verify: %v", err)
	}
	drifts := res.DriftedFiles()
	if len(drifts) != 0 {
		t.Fatalf("expected no drift, got: %v", drifts)
	}
}

// TestCollectAssemblyModulesGenDrift_DetectsTampering verifies that altering
// modules_gen.go on disk is detected as a drift.
func TestCollectAssemblyModulesGenDrift_DetectsTampering(t *testing.T) {
	root := minimalAssemblyProject(t)

	// Tamper with the generated file by appending a byte.
	genPath := filepath.Join(root, "cmd", "myasm", "modules_gen.go")
	content := fileutil.MustReadFile(t, genPath)
	tampered := slices.Concat(content, []byte{'\n'})
	fileutil.MustWriteFile(t, genPath, tampered)

	project, err := parseProject(root)
	if err != nil {
		t.Fatalf("parseProject: %v", err)
	}
	res, err := generateAssemblyModulesGen(root, project, false, true, "")
	if err != nil {
		t.Fatalf("generateAssemblyModulesGen verify: %v", err)
	}
	drifts := res.DriftedFiles()
	if len(drifts) == 0 {
		t.Fatal("expected drift to be detected after tampering, got none")
	}
}

// TestCollectAssemblyModulesGenDrift_DetectsMissing verifies that a missing
// modules_gen.go is reported as a drift.
func TestCollectAssemblyModulesGenDrift_DetectsMissing(t *testing.T) {
	root := minimalAssemblyProject(t)

	// Remove the generated file.
	genPath := filepath.Join(root, "cmd", "myasm", "modules_gen.go")
	if err := os.Remove(genPath); err != nil {
		t.Fatalf("remove modules_gen.go: %v", err)
	}

	project, err := parseProject(root)
	if err != nil {
		t.Fatalf("parseProject: %v", err)
	}
	res, err := generateAssemblyModulesGen(root, project, false, true, "")
	if err != nil {
		t.Fatalf("generateAssemblyModulesGen verify: %v", err)
	}
	drifts := res.DriftedFiles()
	if len(drifts) == 0 {
		t.Fatal("expected drift when modules_gen.go is missing, got none")
	}
}

// TestVerifyCodegenAssembly_LocalNoDrift exercises the full runVerifyCodegenAssembly
// path in --local mode against a clean project.
func TestVerifyCodegenAssembly_LocalNoDrift(t *testing.T) {
	root := minimalAssemblyProject(t)
	chdirToRoot(t, root)
	if err := runVerifyCodegenAssembly(context.Background(), []string{"--local"}); err != nil {
		t.Fatalf("runVerifyCodegenAssembly --local on clean project: %v", err)
	}
}

// TestVerifyCodegenAssembly_LocalDriftWhenFileMissing confirms that --local mode
// returns a drift error when modules_gen.go is absent.
func TestVerifyCodegenAssembly_LocalDriftWhenFileMissing(t *testing.T) {
	root := minimalAssemblyProject(t)
	if err := os.Remove(filepath.Join(root, "cmd", "myasm", "modules_gen.go")); err != nil {
		t.Fatalf("remove modules_gen.go: %v", err)
	}
	chdirToRoot(t, root)
	err := runVerifyCodegenAssembly(context.Background(), []string{"--local"})
	if err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("expected drift error, got %v", err)
	}
}

// TestVerifyCodegenAssembly_UnknownFlag ensures flag-parse errors propagate.
func TestVerifyCodegenAssembly_UnknownFlag(t *testing.T) {
	t.Parallel()
	if err := runVerifyCodegenAssembly(context.Background(), []string{"--bogus"}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

// TestVerifyCodegenAssembly_RejectsPathEscapesRoot asserts that a
// user-controlled assembly.yaml whose build.entrypoint escapes the project
// root cannot coerce verify into reading arbitrary files. The verify path
// shares the codegen.Write IsWithinRoot guard with the write path so a
// crafted "../../../etc/passwd/main.go" entrypoint is rejected before any
// host filesystem access. Regression for review §F3.
func TestVerifyCodegenAssembly_RejectsPathEscapesRoot(t *testing.T) {
	root := minimalAssemblyProject(t)

	// Overwrite assembly.yaml with a malicious build.entrypoint outside the
	// project root. FMT-30 + AssemblyIDPattern stay green; the offending
	// field is the entrypoint path used to derive modules_gen.go's location.
	asmPath := filepath.Join(root, "assemblies", "myasm", "assembly.yaml")
	asmYAML := "id: myasm\ncells:\n  - alpha\n" +
		"owner:\n  team: testteam\n  role: owner\n" +
		"build:\n  entrypoint: ../../../../etc/passwd/main.go\n"
	if err := os.WriteFile(asmPath, []byte(asmYAML), 0o644); err != nil {
		t.Fatalf("rewrite assembly.yaml: %v", err)
	}

	chdirToRoot(t, root)
	err := runVerifyCodegenAssembly(context.Background(), []string{"--local"})
	if err == nil {
		t.Fatal("expected verify to reject path escaping repo root, got nil")
	}
	if !strings.Contains(err.Error(), "escapes RepoRoot") {
		t.Fatalf("expected RepoRoot-escape error, got: %v", err)
	}
}
