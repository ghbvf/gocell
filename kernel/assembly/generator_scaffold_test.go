package assembly

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	asmPath := filepath.Join(root, "assemblies", "myassembly", "assembly.yaml")
	asmYAML, err := os.ReadFile(asmPath) //nolint:gosec // tempdir test fixture
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

// scaffoldTestProject sets up a tempdir project with one cell + go.mod and
// returns (root, parsedProject) ready to feed into NewGenerator.
func scaffoldTestProject(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.Parse: %v", err)
	}
	return root, pm
}

// TestGenerator_Scaffold_ValidationErrors covers the validateAssemblyScaffoldSpec
// branches: empty required fields, invalid deploy, unknown cell ref. Drives
// coverage of validateAssemblyScaffoldSpec + the Scaffold early-return path.
func TestGenerator_Scaffold_ValidationErrors(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	cases := []struct {
		name    string
		spec    AssemblyScaffoldSpec
		wantSub string
	}{
		{
			name:    "empty_id",
			spec:    AssemblyScaffoldSpec{Cells: []string{"examplecell"}, OwnerTeam: "t", OwnerRole: "r"},
			wantSub: "ID is required",
		},
		{
			name:    "empty_cells",
			spec:    AssemblyScaffoldSpec{ID: "asm", OwnerTeam: "t", OwnerRole: "r"},
			wantSub: "at least one cell",
		},
		{
			name:    "empty_team",
			spec:    AssemblyScaffoldSpec{ID: "asm", Cells: []string{"examplecell"}, OwnerRole: "r"},
			wantSub: "OwnerTeam is required",
		},
		{
			name:    "empty_role",
			spec:    AssemblyScaffoldSpec{ID: "asm", Cells: []string{"examplecell"}, OwnerTeam: "t"},
			wantSub: "OwnerRole is required",
		},
		{
			name:    "invalid_deploy",
			spec:    AssemblyScaffoldSpec{ID: "asm", Cells: []string{"examplecell"}, OwnerTeam: "t", OwnerRole: "r", Deploy: "podman"},
			wantSub: `deploy="podman"`,
		},
		{
			name:    "unknown_cell",
			spec:    AssemblyScaffoldSpec{ID: "asm", Cells: []string{"nope"}, OwnerTeam: "t", OwnerRole: "r"},
			wantSub: `cell="nope"`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := gen.Scaffold(tc.spec)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error must contain %q; got: %v", tc.wantSub, err)
			}
		})
	}
}

// TestGenerator_Scaffold_EmptyProjectRoot covers the projectRoot guard at
// the top of Scaffold — generators built without a project root must reject
// scaffold requests since they have nowhere to write.
func TestGenerator_Scaffold_EmptyProjectRoot(t *testing.T) {
	t.Parallel()

	_, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", "") // empty projectRoot

	err := gen.Scaffold(AssemblyScaffoldSpec{
		ID: "asm", Cells: []string{"examplecell"}, OwnerTeam: "t", OwnerRole: "r",
	})
	if err == nil {
		t.Fatal("expected error for empty projectRoot, got nil")
	}
	if !strings.Contains(err.Error(), "projectRoot") {
		t.Errorf("error must mention projectRoot; got: %v", err)
	}
}

// TestGenerator_Scaffold_DryRun covers the dryRun early-return path —
// templates render (catching errors) but no files are written.
func TestGenerator_Scaffold_DryRun(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	if err := gen.Scaffold(AssemblyScaffoldSpec{
		ID: "dryasm", Cells: []string{"examplecell"},
		OwnerTeam: "t", OwnerRole: "r", DryRun: true,
	}); err != nil {
		t.Fatalf("dry-run Scaffold: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assemblies", "dryasm")); err == nil {
		t.Errorf("dry-run wrote assemblies/ to disk")
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "dryasm")); err == nil {
		t.Errorf("dry-run wrote cmd/ to disk")
	}
}

// TestGenerator_Scaffold_ConflictDetection covers the renderAssemblyScaffoldFiles
// pre-existing-file branch: re-running Scaffold against the same id must
// fail-fast before any write.
func TestGenerator_Scaffold_ConflictDetection(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID: "conflict", Cells: []string{"examplecell"},
		OwnerTeam: "t", OwnerRole: "r",
	}
	if err := gen.Scaffold(spec); err != nil {
		t.Fatalf("first Scaffold: %v", err)
	}
	err := gen.Scaffold(spec)
	if err == nil {
		t.Fatal("expected conflict error on second Scaffold, got nil")
	}
	// pathsafe.WritePlannedFiles surfaces the conflicting path via
	// errcode.WithDetails(slog.String("path", ...)) so 4xx responses + CLI
	// stderr expose it (round-4 F16). Assert via structured Details API
	// rather than err.Error() string match — see ADR §errcode three-layer
	// redaction (Details vs Internal).
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error must unwrap to *errcode.Error; got %T: %v", err, err)
	}
	pathAttr, ok := ec.FindAttr("path")
	if !ok {
		t.Fatalf("conflict error must carry 'path' detail; got %+v", ec.Details)
	}
	if !strings.Contains(pathAttr.Value.String(), "conflict") {
		t.Errorf("path detail must reference the conflicting assembly id; got %q", pathAttr.Value.String())
	}
}

// ---------------------------------------------------------------------------
// Symlink escape + atomic rollback tests (RED — 实现漏斗化后转 GREEN)
// ---------------------------------------------------------------------------

// TestGeneratorScaffold_SymlinkEscape_Asm 验证 Generator.Scaffold 拒绝
// assemblies/<id> 目录是 root 外 symlink 的情况。
func TestGeneratorScaffold_SymlinkEscape_Asm(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
	outside := t.TempDir()

	// assemblies/symasm → outside
	assembliesDir := filepath.Join(root, "assemblies")
	if err := os.MkdirAll(assembliesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assembliesDir, "symasm")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := gen.Scaffold(AssemblyScaffoldSpec{
		ID:        "symasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err == nil {
		t.Fatal("Generator.Scaffold(asm symlink escape): want error, got nil")
	}

	// outside 不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("asm symlink escape: outside must be clean, got %v", entries)
	}
}

// TestGeneratorScaffold_SymlinkEscape_Cmd 验证 Generator.Scaffold 拒绝
// cmd/<id> 目录是 root 外 symlink 的情况。
func TestGeneratorScaffold_SymlinkEscape_Cmd(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
	outside := t.TempDir()

	// cmd/symcmdasm → outside
	cmdDir := filepath.Join(root, "cmd")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cmdDir, "symcmdasm")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := gen.Scaffold(AssemblyScaffoldSpec{
		ID:        "symcmdasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err == nil {
		t.Fatal("Generator.Scaffold(cmd symlink escape): want error, got nil")
	}

	// outside 不应有任何文件
	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("cmd symlink escape: outside must be clean, got %v", entries)
	}
}

// TestGeneratorScaffold_AtomicRollback_OnConflict 验证：
// 预置 assemblies/<id>/assembly.yaml 冲突时，cmd/<id> 不被创建（atomic）。
func TestGeneratorScaffold_AtomicRollback_OnConflict(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	// 预置冲突 assembly.yaml
	asmDir := filepath.Join(root, "assemblies", "conflictasm")
	if err := os.MkdirAll(asmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asmDir, "assembly.yaml"), []byte("id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := gen.Scaffold(AssemblyScaffoldSpec{
		ID:        "conflictasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err == nil {
		t.Fatal("Generator.Scaffold(conflict): want error, got nil")
	}

	// atomic：cmd/conflictasm 不应存在
	if _, err := os.Stat(filepath.Join(root, "cmd", "conflictasm")); err == nil {
		t.Error("atomic rollback: cmd/conflictasm must not exist after conflict error")
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

	asmPath := filepath.Join(root, "assemblies", "myassembly", "assembly.yaml")
	asmYAML, _ := os.ReadFile(asmPath) //nolint:gosec // tempdir test fixture
	got := string(asmYAML)
	if !strings.Contains(got, "deployTemplate: compose") {
		t.Errorf("--deploy=compose should write deployTemplate; got:\n%s", got)
	}
}
