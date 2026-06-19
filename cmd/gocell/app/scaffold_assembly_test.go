package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
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

// TestRunScaffoldAssembly_OwnerTextRule asserts that --team and --role
// containing control characters (\n, \r, \x00) are rejected with
// ERR_SCAFFOLD_INVALID_OPTS at the CLI flag-validation layer.
func TestRunScaffoldAssembly_OwnerTextRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		team string
		role string
	}{
		{"team_lf_rejected", "alice\nbob", "maintainer"},
		{"team_cr_rejected", "alice\rbob", "maintainer"},
		{"team_nul_rejected", "alice\x00bob", "maintainer"},
		{"role_lf_rejected", "platform", "evil\nrole"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := setupAssemblyTestProject(t, "examplecell")
			args := []string{
				"--id=myassembly",
				"--cells=examplecell",
				"--team=" + tc.team,
				"--role=" + tc.role,
			}
			err := scaffoldAssembly(root, args)
			if err == nil {
				t.Fatal("expected error for control char in owner field, got nil")
			}
			if !strings.Contains(err.Error(), string(ErrScaffoldInvalidOpts)) {
				t.Errorf("error must contain %q; got: %v", ErrScaffoldInvalidOpts, err)
			}
		})
	}

	// Positive: ascii values accepted.
	t.Run("ascii_accepted", func(t *testing.T) {
		t.Parallel()
		root := setupAssemblyTestProject(t, "examplecell")
		args := []string{
			"--id=myassembly",
			"--cells=examplecell",
			"--team=platform-team",
			"--role=maintainer",
		}
		if err := scaffoldAssembly(root, args); err != nil {
			t.Fatalf("ascii team/role must be accepted, got: %v", err)
		}
	})
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
		"cmd/siasm/app.go",
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

// ---------------------------------------------------------------------------
// B：round-6 新增 CLI 测试（RED — scaffoldAssembly 尚未使用 PlanAssemblyScaffold）
// ---------------------------------------------------------------------------

// sixFileRels 是 round-6 完整 plan 的 6 个相对路径（forward-slash）。
func sixFileRels(id string) []string {
	return []string{
		fmt.Sprintf("assemblies/%s/assembly.yaml", id),
		fmt.Sprintf("cmd/%s/run.go", id),
		fmt.Sprintf("cmd/%s/app.go", id),
		fmt.Sprintf("cmd/%s/modules_gen.go", id),
		fmt.Sprintf("cmd/%s/main.go", id),
		fmt.Sprintf("assemblies/%s/generated/boundary.yaml", id),
	}
}

// TestRunScaffoldAssembly_DryRun_PrintsAllSixPaths 验证 dry-run 输出 6 行
// "(dry-run) Would create ..." 并且 0 文件落盘。
// RED：scaffoldAssembly 尚未调用 PlanAssemblyScaffold，dry-run 只打 3 行。
func TestRunScaffoldAssembly_DryRun_PrintsAllSixPaths(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=dryrunasm",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
		"--dry-run",
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = scaffoldAssembly(root, args)
	})
	if runErr != nil {
		t.Fatalf("scaffoldAssembly dry-run: %v", runErr)
	}

	wantRels := sixFileRels("dryrunasm")
	for _, rel := range wantRels {
		wantLine := fmt.Sprintf("(dry-run) Would create %s", rel)
		if !strings.Contains(out, wantLine) {
			t.Errorf("dry-run output missing %q\nfull output:\n%s", wantLine, out)
		}
	}

	// 确认 0 文件落盘
	for _, rel := range wantRels {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr == nil {
			t.Errorf("dry-run: file must not exist on disk: %s", rel)
		}
	}
}

// TestRunScaffoldAssembly_LiveRollback_OnSecondStageConflict pre-place
// cmd/<id>/main.go → 跑 live 模式 → 断言 err 含 conflict + assembly.yaml /
// run.go / app.go 全部不存在（跨阶段 rollback）。
// RED：scaffoldAssembly 尚未使用单 plan，无法做跨阶段 rollback。
func TestRunScaffoldAssembly_LiveRollback_OnSecondStageConflict(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	// 预置 cmd/<id>/main.go 制造第 5 个槽冲突
	cmdDir := filepath.Join(root, "cmd", "liverollback")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--id=liverollback",
		"--cells=examplecell",
		"--team=platform",
		"--role=maintainer",
	}
	err := scaffoldAssembly(root, args)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "conflict") &&
		!strings.Contains(strings.ToLower(err.Error()), "exist") {
		t.Errorf("error must indicate conflict; got: %v", err)
	}

	// skeleton 文件（前三）不应存在 — all-or-nothing rollback
	for _, rel := range []string{
		filepath.Join("assemblies", "liverollback", "assembly.yaml"),
		filepath.Join("cmd", "liverollback", "run.go"),
		filepath.Join("cmd", "liverollback", "app.go"),
	} {
		if _, statErr := os.Stat(filepath.Join(root, rel)); statErr == nil {
			t.Errorf("rollback: file must not exist: %s", rel)
		}
	}
}

// TestRunScaffoldAssembly_IDMetadataRule asserts that scaffold assembly's
// --id flag routes through kernel/metadata.MatchAssemblyID single-source
// pattern (^[a-z][a-z0-9]+$) — rejecting kebab-case, capitalised, and
// digit-leading IDs that the legacy path-traversal blacklist used to accept.
// This is the cmd-layer side of SCAFFOLD-ASSEMBLY-ID-METADATA-RULE-01.
//
// ref: kubernetes/apimachinery IsDNS1123Label — same single-helper validation
// dispatched from CLI flag layer.
func TestRunScaffoldAssembly_IDMetadataRule(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	cases := []struct {
		name      string
		id        string
		wantValid bool
	}{
		{"valid_id", "myassembly", true},
		{"kebab_rejected", "my-assembly", false},
		{"capitalised_rejected", "MyAssembly", false},
		{"digit_start_rejected", "9asm", false},
		{"single_char_rejected", "a", false},
		{"underscore_rejected", "my_asm", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{
				"--id=" + tc.id,
				"--cells=examplecell",
				"--team=platform",
				"--role=maintainer",
				"--dry-run",
			}
			err := scaffoldAssembly(root, args)
			gotValid := err == nil
			if gotValid != tc.wantValid {
				t.Fatalf("scaffoldAssembly(--id=%q) valid=%v err=%v; want valid=%v",
					tc.id, gotValid, err, tc.wantValid)
			}
			if !tc.wantValid && !strings.Contains(err.Error(), "ERR_SCAFFOLD_INVALID_OPTS") {
				t.Errorf("expected ERR_SCAFFOLD_INVALID_OPTS in error; got %v", err)
			}
		})
	}
}

// TestRunScaffoldAssembly_CellsMetadataRule asserts --cells[] elements route
// through kernel/metadata.MatchCellID (same pattern as MatchAssemblyID by
// design — FMT-16 / FMT-C1 no-dash convention). Note: cmd flag layer routes
// invalid cell ids first through pattern rejection; existence check applies
// only to syntactically valid IDs.
func TestRunScaffoldAssembly_CellsMetadataRule(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	cases := []struct {
		name      string
		cells     string
		wantValid bool
	}{
		{"valid_cell", "examplecell", true},
		{"kebab_rejected", "my-cell", false},
		{"capitalised_rejected", "MyCell", false},
		{"digit_start_rejected", "9cell", false},
		{"underscore_rejected", "my_cell", false},
		{"second_cell_invalid", "examplecell,9bad", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{
				"--id=myassembly",
				"--cells=" + tc.cells,
				"--team=platform",
				"--role=maintainer",
				"--dry-run",
			}
			err := scaffoldAssembly(root, args)
			gotValid := err == nil
			if gotValid != tc.wantValid {
				t.Fatalf("scaffoldAssembly(--cells=%q) valid=%v err=%v; want valid=%v",
					tc.cells, gotValid, err, tc.wantValid)
			}
			if !tc.wantValid && !strings.Contains(err.Error(), "ERR_SCAFFOLD_INVALID_OPTS") {
				t.Errorf("expected ERR_SCAFFOLD_INVALID_OPTS in error; got %v", err)
			}
		})
	}
}

// setupAssemblyTestProject creates a tempdir project with go.mod and the
// supplied cell skeleton (cell.yaml only — sufficient for assembly scaffold
// validation).
func setupAssemblyTestProject(t *testing.T, cellID string) string {
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

// writeAssemblyTestCell adds a cell.yaml skeleton for cellID under an existing
// project root (companion to setupAssemblyTestProject for multi-cell fixtures).
func writeAssemblyTestCell(t *testing.T, root, cellID string) {
	t.Helper()
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
}

// ---------------------------------------------------------------------------
// #1516 cross-module --cell flag (RED — scaffoldAssembly has no --cell flag yet)
// ---------------------------------------------------------------------------

const crossModuleMDM = "github.com/ghbvf/gocell-mdm"

// TestRunScaffoldAssembly_CellFlag_SameModule asserts the new repeatable --cell
// flag with a bare id (no @module) is the same-module form: behaves identically
// to --cells=<id> (skeleton + K#10 derived files, no compositionAPI).
func TestRunScaffoldAssembly_CellFlag_SameModule(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=samemod",
		"--cell=examplecell",
		"--team=platform",
		"--role=maintainer",
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly --cell same-module: %v", err)
	}
	// Full 6-file plan (same-module is unchanged behavior).
	for _, rel := range sixFileRels("samemod") {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("same-module --cell missing %s: %v", rel, err)
		}
	}
	asmYAML, _ := os.ReadFile(filepath.Join(root, "assemblies", "samemod", "assembly.yaml")) //nolint:gosec // tempdir test fixture
	if strings.Contains(string(asmYAML), "module:") || strings.Contains(string(asmYAML), "compositionAPI") {
		t.Errorf("same-module --cell must not emit module/compositionAPI; got:\n%s", asmYAML)
	}
}

// TestRunScaffoldAssembly_CellFlag_CrossModule asserts that --cell id@module
// emits object-form cells[].{id,module} + build.compositionAPI: true, and that
// cross-module entries auto-skip the K#10 derived files (cross-module cell
// metadata is not locally resolvable — #1515 boundary) with an actionable hint.
func TestRunScaffoldAssembly_CellFlag_CrossModule(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	args := []string{
		"--id=mdm",
		"--cell=examplecell",
		"--cell=enrollcell@" + crossModuleMDM,
		"--team=mdm",
		"--role=maintainer",
	}
	var runErr error
	out := captureStdout(t, func() { runErr = scaffoldAssembly(root, args) })
	if runErr != nil {
		t.Fatalf("scaffoldAssembly --cell cross-module: %v", runErr)
	}

	asmYAML, err := os.ReadFile(filepath.Join(root, "assemblies", "mdm", "assembly.yaml")) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read assembly.yaml: %v", err)
	}
	got := string(asmYAML)
	for _, want := range []string{"enrollcell", crossModuleMDM, "compositionAPI: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("cross-module assembly.yaml missing %q; got:\n%s", want, got)
		}
	}

	// Cross-module ⟹ K#10 derived files auto-skipped; skeleton still written.
	for _, rel := range []string{"assemblies/mdm/assembly.yaml", "cmd/mdm/run.go", "cmd/mdm/app.go"} {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr != nil {
			t.Errorf("cross-module skeleton missing %s: %v", rel, statErr)
		}
	}
	for _, rel := range []string{"cmd/mdm/modules_gen.go", "cmd/mdm/main.go", "assemblies/mdm/generated/boundary.yaml"} {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr == nil {
			t.Errorf("cross-module must auto-skip derived file %s, but it exists", rel)
		}
	}
	if !strings.Contains(out, "gocell generate assembly") {
		t.Errorf("cross-module scaffold must print an actionable generate hint; got:\n%s", out)
	}
}

// TestRunScaffoldAssembly_CrossModule_RoundTripParse is the correctness guard
// for YAML synthesis: re-parse the generated assembly.yaml and assert the
// AssemblyCellRef{ID,Module} set + Build.CompositionAPI round-trip exactly
// (stronger than string-contains; catches flow-mapping/quoting drift).
func TestRunScaffoldAssembly_CrossModule_RoundTripParse(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")
	writeAssemblyTestCell(t, root, "enrollcell") // present locally (workspace mode) so full-project Parse resolves

	args := []string{
		"--id=mdm",
		"--cell=examplecell",
		"--cell=enrollcell@" + crossModuleMDM,
		"--team=mdm",
		"--role=maintainer",
		"--skip-generate", // round-trip only needs the assembly.yaml
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly cross-module round-trip: %v", err)
	}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("re-parse project: %v", err)
	}
	asm := project.Assemblies["mdm"]
	if asm == nil {
		t.Fatalf("parsed project missing assembly mdm; assemblies=%v", project.Assemblies)
	}
	want := []metadata.AssemblyCellRef{
		{ID: "examplecell"},
		{ID: "enrollcell", Module: crossModuleMDM},
	}
	if len(asm.Cells) != len(want) {
		t.Fatalf("round-trip cells len=%d want %d (%v)", len(asm.Cells), len(want), asm.Cells)
	}
	for i, w := range want {
		if asm.Cells[i].ID != w.ID || asm.Cells[i].Module != w.Module {
			t.Errorf("round-trip cells[%d]=%+v want %+v", i, asm.Cells[i], w)
		}
	}
	if !asm.Build.CompositionAPI {
		t.Errorf("round-trip: cross-module assembly must parse back with build.compositionAPI=true")
	}
}

// TestRunScaffoldAssembly_CellFlag_BadModule rejects malformed module strings
// (control/space/quote/backslash) and an empty module after '@'.
func TestRunScaffoldAssembly_CellFlag_BadModule(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	cases := []struct {
		name string
		cell string
	}{
		{"space_in_module", "examplecell@github.com/acme/bad cell"},
		{"quote_in_module", "examplecell@github.com/acme/\"x"},
		{"backslash_in_module", "examplecell@github.com/acme/x\\y"},
		{"empty_module_after_at", "examplecell@"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := scaffoldAssembly(root, []string{
				"--id=mdm", "--cell=" + tc.cell, "--team=mdm", "--role=maintainer", "--dry-run",
			})
			if err == nil {
				t.Fatalf("expected rejection for --cell=%q, got nil", tc.cell)
			}
			if !strings.Contains(err.Error(), "ERR_SCAFFOLD_INVALID_OPTS") {
				t.Errorf("expected ERR_SCAFFOLD_INVALID_OPTS; got %v", err)
			}
		})
	}
}

// TestRunScaffoldAssembly_DuplicateCells rejects duplicate cell ids within
// either flag, naming the offending cell.
func TestRunScaffoldAssembly_DuplicateCells(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	cases := []struct {
		name string
		args []string
	}{
		{"dup_in_cell", []string{"--cell=examplecell", "--cell=examplecell"}},
		{"dup_in_cells", []string{"--cells=examplecell,examplecell"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := append([]string{"--id=mdm", "--team=mdm", "--role=maintainer", "--dry-run"}, tc.args...)
			err := scaffoldAssembly(root, args)
			if err == nil {
				t.Fatalf("expected duplicate-cell rejection, got nil")
			}
			if !strings.Contains(err.Error(), "examplecell") {
				t.Errorf("duplicate error must name the cell; got %v", err)
			}
		})
	}
}

// TestRunScaffoldAssembly_CellsAndCellMutuallyExclusive rejects supplying both
// --cells and --cell (ambiguous startup order).
func TestRunScaffoldAssembly_CellsAndCellMutuallyExclusive(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	err := scaffoldAssembly(root, []string{
		"--id=mdm", "--cells=examplecell", "--cell=examplecell",
		"--team=mdm", "--role=maintainer", "--dry-run",
	})
	if err == nil {
		t.Fatal("expected mutual-exclusivity rejection, got nil")
	}
	if !strings.Contains(err.Error(), "ERR_SCAFFOLD_INVALID_OPTS") {
		t.Errorf("expected ERR_SCAFFOLD_INVALID_OPTS; got %v", err)
	}
}

// TestRunScaffoldAssembly_NeitherCellsNorCell rejects supplying neither flag.
func TestRunScaffoldAssembly_NeitherCellsNorCell(t *testing.T) {
	t.Parallel()
	root := setupAssemblyTestProject(t, "examplecell")

	err := scaffoldAssembly(root, []string{
		"--id=mdm", "--team=mdm", "--role=maintainer", "--dry-run",
	})
	if err == nil {
		t.Fatal("expected error when neither --cells nor --cell is given, got nil")
	}
}
