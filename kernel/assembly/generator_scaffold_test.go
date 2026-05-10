package assembly

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// 目标路径常量，避免同义字面量重复。
const (
	scaffoldAsmYAML    = "assembly.yaml"
	scaffoldRunGo      = "run.go"
	scaffoldAppGo      = "app.go"
	scaffoldModulesGen = "modules_gen.go"
	scaffoldMainGo     = "main.go"
	scaffoldBoundary   = "boundary.yaml"
)

// scaffoldSixPaths 返回六文件 plan 中各文件相对 root 的路径（按方案顺序）。
func scaffoldSixPaths(id string) []string {
	return []string{
		filepath.Join("assemblies", id, scaffoldAsmYAML),
		filepath.Join("cmd", id, scaffoldRunGo),
		filepath.Join("cmd", id, scaffoldAppGo),
		filepath.Join("cmd", id, scaffoldModulesGen),
		filepath.Join("cmd", id, scaffoldMainGo),
		filepath.Join("assemblies", id, "generated", scaffoldBoundary),
	}
}

// scaffoldThreePaths 返回 skeleton 三文件相对 root 的路径。
func scaffoldThreePaths(id string) []string {
	return scaffoldSixPaths(id)[:3]
}

// TestGenerator_Scaffold 迁移到 plan + WritePlannedFiles 模式（round-6）。
// RED：PlanAssemblyScaffold 未实现时本测试编译失败。
func TestGenerator_Scaffold(t *testing.T) {
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
		Deploy:    "k8s",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("Generator.PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles: %v", err)
	}

	for _, rel := range scaffoldThreePaths("myassembly") {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr != nil {
			t.Errorf("scaffold missing %s: %v", rel, statErr)
		}
	}

	asmPath := filepath.Join(root, "assemblies", "myassembly", scaffoldAsmYAML)
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

// TestGenerator_Scaffold_ValidationErrors 迁移到 PlanAssemblyScaffold 模式。
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
			_, err := gen.PlanAssemblyScaffold(tc.spec)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error must contain %q; got: %v", tc.wantSub, err)
			}
		})
	}
}

// TestGenerator_Scaffold_EmptyProjectRoot 迁移到 PlanAssemblyScaffold 模式。
func TestGenerator_Scaffold_EmptyProjectRoot(t *testing.T) {
	t.Parallel()

	_, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", "") // empty projectRoot

	_, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID: "asm", Cells: []string{"examplecell"}, OwnerTeam: "t", OwnerRole: "r",
	})
	if err == nil {
		t.Fatal("expected error for empty projectRoot, got nil")
	}
	if !strings.Contains(err.Error(), "projectRoot") {
		t.Errorf("error must mention projectRoot; got: %v", err)
	}
}

// TestGenerator_Scaffold_DryRun 迁移：用 WritePlannedFiles(true) 模拟 dry-run。
func TestGenerator_Scaffold_DryRun(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID: "dryasm", Cells: []string{"examplecell"},
		OwnerTeam: "t", OwnerRole: "r",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold dry-run: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, true); err != nil {
		t.Fatalf("WritePlannedFiles(dryRun=true): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assemblies", "dryasm")); err == nil {
		t.Errorf("dry-run wrote assemblies/ to disk")
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "dryasm")); err == nil {
		t.Errorf("dry-run wrote cmd/ to disk")
	}
}

// TestGenerator_Scaffold_ConflictDetection 迁移到 plan + WritePlannedFiles 模式。
func TestGenerator_Scaffold_ConflictDetection(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	spec := AssemblyScaffoldSpec{
		ID: "conflict", Cells: []string{"examplecell"},
		OwnerTeam: "t", OwnerRole: "r",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("first PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, false); err != nil {
		t.Fatalf("first WritePlannedFiles: %v", err)
	}

	plan2, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("second PlanAssemblyScaffold: %v", err)
	}
	err = pathsafe.WritePlannedFiles(realRoot, plan2, false)
	if err == nil {
		t.Fatal("expected conflict error on second WritePlannedFiles, got nil")
	}
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
// Symlink escape + atomic rollback tests（迁移到 plan + WritePlannedFiles）
// ---------------------------------------------------------------------------

// TestGeneratorScaffold_SymlinkEscape_Asm 迁移到 plan + WritePlannedFiles 模式。
func TestGeneratorScaffold_SymlinkEscape_Asm(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
	outside := t.TempDir()

	assembliesDir := filepath.Join(root, "assemblies")
	if err := os.MkdirAll(assembliesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assembliesDir, "symasm")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "symasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err == nil {
		// If PlanAssemblyScaffold doesn't detect it, WritePlannedFiles should.
		realRoot, _ := pathsafe.ResolveRoot(root)
		err = pathsafe.WritePlannedFiles(realRoot, plan, false)
	}
	if err == nil {
		t.Fatal("Generator.PlanAssemblyScaffold(asm symlink escape): want error, got nil")
	}

	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("asm symlink escape: outside must be clean, got %v", entries)
	}
}

// TestGeneratorScaffold_SymlinkEscape_Cmd 迁移到 plan + WritePlannedFiles 模式。
func TestGeneratorScaffold_SymlinkEscape_Cmd(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
	outside := t.TempDir()

	cmdDir := filepath.Join(root, "cmd")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cmdDir, "symcmdasm")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "symcmdasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err == nil {
		realRoot, _ := pathsafe.ResolveRoot(root)
		err = pathsafe.WritePlannedFiles(realRoot, plan, false)
	}
	if err == nil {
		t.Fatal("Generator.PlanAssemblyScaffold(cmd symlink escape): want error, got nil")
	}

	entries, _ := os.ReadDir(outside)
	if len(entries) > 0 {
		t.Errorf("cmd symlink escape: outside must be clean, got %v", entries)
	}
}

// TestGeneratorScaffold_AtomicRollback_OnConflict 迁移到 plan + WritePlannedFiles 模式。
func TestGeneratorScaffold_AtomicRollback_OnConflict(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	asmDir := filepath.Join(root, "assemblies", "conflictasm")
	if err := os.MkdirAll(asmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asmDir, scaffoldAsmYAML), []byte("id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "conflictasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	err = pathsafe.WritePlannedFiles(realRoot, plan, false)
	if err == nil {
		t.Fatal("Generator(conflict): want error, got nil")
	}

	// atomic：cmd/conflictasm 不应存在
	if _, statErr := os.Stat(filepath.Join(root, "cmd", "conflictasm")); statErr == nil {
		t.Error("atomic rollback: cmd/conflictasm must not exist after conflict error")
	}
}

// TestGenerator_Scaffold_DeployCompose 迁移到 plan + WritePlannedFiles 模式。
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

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "myassembly",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
		Deploy:    "compose",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles: %v", err)
	}

	asmPath := filepath.Join(root, "assemblies", "myassembly", scaffoldAsmYAML)
	asmYAML, _ := os.ReadFile(asmPath) //nolint:gosec // tempdir test fixture
	got := string(asmYAML)
	if !strings.Contains(got, "deployTemplate: compose") {
		t.Errorf("--deploy=compose should write deployTemplate; got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// A.2 新增测试（RED — PlanAssemblyScaffold + SkipGenerate 未实现时失败）
// ---------------------------------------------------------------------------

// TestPlanAssemblyScaffold_FullPlan_SixFiles 验证默认 plan 含 6 个 PlannedFile，
// 顺序为 [assembly.yaml, run.go, app.go, modules_gen.go, main.go, boundary.yaml]。
func TestPlanAssemblyScaffold_FullPlan_SixFiles(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "sixasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	if len(plan) != 6 {
		t.Fatalf("expected 6 PlannedFile, got %d", len(plan))
	}

	wantRels := scaffoldSixPaths("sixasm")
	realRoot, _ := pathsafe.ResolveRoot(root)
	for i, rel := range wantRels {
		wantAbs := filepath.Join(realRoot, rel)
		if plan[i].AbsPath != wantAbs {
			t.Errorf("plan[%d]: want AbsPath=%q, got %q", i, wantAbs, plan[i].AbsPath)
		}
	}
}

// TestPlanAssemblyScaffold_SkipGenerate_ThreeFiles 验证 SkipGenerate=true 时
// plan 只含 3 个 skeleton PlannedFile。
func TestPlanAssemblyScaffold_SkipGenerate_ThreeFiles(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:           "skipasm",
		Cells:        []string{"examplecell"},
		OwnerTeam:    "platform",
		OwnerRole:    "maintainer",
		SkipGenerate: true,
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold(SkipGenerate=true): %v", err)
	}
	if len(plan) != 3 {
		t.Fatalf("SkipGenerate=true: expected 3 PlannedFile, got %d", len(plan))
	}

	wantRels := scaffoldThreePaths("skipasm")
	realRoot, _ := pathsafe.ResolveRoot(root)
	for i, rel := range wantRels {
		wantAbs := filepath.Join(realRoot, rel)
		if plan[i].AbsPath != wantAbs {
			t.Errorf("plan[%d]: want AbsPath=%q, got %q", i, wantAbs, plan[i].AbsPath)
		}
	}
}

// TestPlanAssemblyScaffold_DryRun_NoFilesOnDisk 验证：用 plan + WritePlannedFiles(true)
// 后 6 个文件全部不存在于工作树。
func TestPlanAssemblyScaffold_DryRun_NoFilesOnDisk(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "drysixasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, true); err != nil {
		t.Fatalf("WritePlannedFiles(dryRun=true): %v", err)
	}

	for _, rel := range scaffoldSixPaths("drysixasm") {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("dry-run: file must not exist: %s", rel)
		}
	}
}

// TestPlanAssemblyScaffold_GeneratedFilesHaveMarker 验证派生 3 文件
// (modules_gen.go / main.go / boundary.yaml) 内容前缀通过
// governance.IsGoCellGenerated。
func TestPlanAssemblyScaffold_GeneratedFilesHaveMarker(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "markerasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	if len(plan) != 6 {
		t.Fatalf("expected 6 PlannedFile, got %d", len(plan))
	}

	// plan[3]=modules_gen.go, plan[4]=main.go, plan[5]=boundary.yaml
	generatedSlots := []struct {
		idx  int
		name string
	}{
		{3, scaffoldModulesGen},
		{4, scaffoldMainGo},
		{5, scaffoldBoundary},
	}
	for _, s := range generatedSlots {
		if !governance.IsGoCellGenerated(plan[s.idx].Content) {
			t.Errorf("plan[%d] (%s): content missing gocell generated marker; prefix=%q",
				s.idx, s.name, string(plan[s.idx].Content[:min(64, len(plan[s.idx].Content))]))
		}
	}
}

// TestPlanAssemblyScaffold_FullPlan_RollbackOnLastFileConflict 验证：
// pre-place assemblies/<id>/generated/boundary.yaml → WritePlannedFiles 失败 →
// 断言前 5 个文件全部不存在（all-or-nothing rollback）+ err 通过 errors.As 解到
// *errcode.Error 且 Kind=KindConflict。
func TestPlanAssemblyScaffold_FullPlan_RollbackOnLastFileConflict(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	// 预置冲突：boundary.yaml（第 6 个文件槽）
	boundaryDir := filepath.Join(root, "assemblies", "rollbackasm", "generated")
	if err := os.MkdirAll(boundaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundaryDir, scaffoldBoundary), []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
		ID:        "rollbackasm",
		Cells:     []string{"examplecell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
	})
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	err = pathsafe.WritePlannedFiles(realRoot, plan, false)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error must unwrap to *errcode.Error; got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindConflict {
		t.Errorf("error Kind must be KindConflict; got %v", ec.Kind)
	}

	// 全部前 5 个不应存在（rollback）
	noPaths := scaffoldSixPaths("rollbackasm")[:5]
	for _, rel := range noPaths {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("rollback: file must not exist: %s", rel)
		}
	}
}

// TestPlanAssemblyScaffold_ConflictDetection_AllSixSlots 验证 6 个 slot 各自
// pre-place 后 WritePlannedFiles 都失败，且其他 5 个 slot 不存在。
func TestPlanAssemblyScaffold_ConflictDetection_AllSixSlots(t *testing.T) {
	t.Parallel()

	rels := scaffoldSixPaths("slotasm")
	// 预计写入目录列表（需要 MkdirAll 才能放 fixture）
	parentDirs := []string{
		filepath.Join("assemblies", "slotasm"),
		filepath.Join("cmd", "slotasm"),
		filepath.Join("cmd", "slotasm"),
		filepath.Join("cmd", "slotasm"),
		filepath.Join("cmd", "slotasm"),
		filepath.Join("assemblies", "slotasm", "generated"),
	}

	for slotIdx := 0; slotIdx < 6; slotIdx++ {
		slotIdx := slotIdx
		t.Run(rels[slotIdx], func(t *testing.T) {
			t.Parallel()

			root, pm := scaffoldTestProject(t)
			gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

			// 预置第 slotIdx 个文件
			dir := filepath.Join(root, parentDirs[slotIdx])
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(rels[slotIdx])
			if err := os.WriteFile(filepath.Join(root, rels[slotIdx]), []byte("# preexisting\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = name

			plan, err := gen.PlanAssemblyScaffold(AssemblyScaffoldSpec{
				ID:        "slotasm",
				Cells:     []string{"examplecell"},
				OwnerTeam: "platform",
				OwnerRole: "maintainer",
			})
			if err != nil {
				t.Fatalf("PlanAssemblyScaffold: %v", err)
			}
			realRoot, _ := pathsafe.ResolveRoot(root)
			writeErr := pathsafe.WritePlannedFiles(realRoot, plan, false)
			if writeErr == nil {
				t.Fatal("expected conflict error, got nil")
			}

			// 其他 5 个 slot 不应存在
			for otherIdx, otherRel := range rels {
				if otherIdx == slotIdx {
					continue // 预置的不检查
				}
				if _, statErr := os.Stat(filepath.Join(root, otherRel)); statErr == nil {
					t.Errorf("slot %d conflict: file must not exist: %s", slotIdx, otherRel)
				}
			}
		})
	}
}

// min 是 Go 1.21 前的 int 版本，保留兼容。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
