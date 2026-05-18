package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// TestScaffoldCell_GoldenCellGo is an anti-drift gate for the scaffold-cell
// template (tools/codegen/cellgen/templates/scaffold-cell.tmpl). It scaffolds
// a cell with a fixed spec and asserts that the generated cell.go contains
// every marker and pattern required by the K#04/K#05 conventions.
//
// Failures here mean the scaffold template drifted from the required patterns;
// update the template — not this test.
func TestScaffoldCell_GoldenCellGo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := cellgen.ScaffoldSpec{
		CellID:           mustID(t, "goldcell"),
		StructName:       "GoldCell",
		Package:          "goldcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}

	if err := cellgen.ScaffoldCell(dir, "cells/goldcell", spec); err != nil {
		t.Fatalf("ScaffoldCell: %v", err)
	}

	cellGoPath := filepath.Join(dir, "cells", "goldcell", "cell.go")
	content := fileutil.MustReadFile(t, cellGoPath)
	got := string(content)

	// Required patterns — each entry is a load-bearing invariant.
	// Modifying this list requires a corresponding template change.
	required := []struct {
		pattern string
		reason  string
	}{
		// K#04: loadCellMetadata() pattern — no metadata literal in cell.go.
		{"loadCellMetadata()", "K#04: cell.go must use loadCellMetadata() — not a CellMeta{} literal"},
		// K#05: // +cell:listener: marker drives cellgen code generation.
		{"// +cell:listener:", "K#05: // +cell:listener: marker required for cellgen route wiring"},
		// K#05: initInternal hook — cellgen calls this after generated Init.
		{"func (c *GoldCell) initInternal(", "K#04: initInternal hook required; cellgen::Init calls it"},
		// K#04: cell.BaseCell embedded — structural requirement.
		{"*cell.BaseCell", "K#04: *cell.BaseCell must be embedded in the cell struct"},
		// Package declaration matches CellID (no dash — FMT-C1 enforced).
		{"package goldcell", "package name must match CellID with dashes stripped"},
		// Constructor uses the generated loadCellMetadata, not a raw literal.
		{"NewGoldCell()", "constructor must be exported and use loadCellMetadata"},
		// Context import required by initInternal signature.
		{"context", "context import required for initInternal(ctx context.Context, ...)"},
	}

	for _, r := range required {
		if !strings.Contains(got, r.pattern) {
			t.Errorf("scaffold golden drift: cell.go missing %q\n  reason: %s\n  got:\n%s",
				r.pattern, r.reason, got)
		}
	}
}

// TestScaffoldCell_GoldenCellYAML is the companion gate for cell.yaml output.
// It verifies that the scaffolded cell.yaml contains the fields required by
// gocell validate and the K#04 single-source-of-truth invariant.
func TestScaffoldCell_GoldenCellYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := cellgen.ScaffoldSpec{
		CellID:           mustID(t, "goldcell"),
		StructName:       "GoldCell",
		Package:          "goldcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
	}

	if err := cellgen.ScaffoldCell(dir, "cells/goldcell", spec); err != nil {
		t.Fatalf("ScaffoldCell: %v", err)
	}

	cellYAMLPath := filepath.Join(dir, "cells", "goldcell", "cell.yaml")
	content := fileutil.MustReadFile(t, cellYAMLPath)
	got := string(content)

	required := []struct {
		pattern string
		reason  string
	}{
		{"consistencyLevel: L2", "K-06: consistencyLevel must appear at top-level of cell.yaml"},
		{"goStructName: GoldCell", "K#04: goStructName required for cellgen to find the struct"},
		{"id: goldcell", "id must match the CellID argument"},
		{"type: core", "type must be rendered from spec.Type"},
		{"team: platform", "owner.team must come from spec.OwnerTeam"},
		{"role: cell-owner", "owner.role must come from spec.OwnerRole — no TODO placeholder"},
		{"smoke.goldcell.startup", "verify.smoke must include a startup entry derived from CellID"},
	}

	for _, r := range required {
		if !strings.Contains(got, r.pattern) {
			t.Errorf("scaffold golden drift: cell.yaml missing %q\n  reason: %s\n  got:\n%s",
				r.pattern, r.reason, got)
		}
	}

	// Anti-pattern: must NOT contain TODO placeholders for role.
	if strings.Contains(got, "role: TODO") {
		t.Error("scaffold golden drift: cell.yaml must not contain 'role: TODO' placeholder")
	}
	// Anti-pattern: must NOT contain metadata literal (single-source invariant).
	if strings.Contains(got, "CellMeta{") {
		t.Error("scaffold golden drift: cell.go must not contain CellMeta{} literal — use loadCellMetadata()")
	}
}

// TestScaffoldAssembly_GoldenSixFiles 是 round-6 六文件 plan 的 golden gate。
// 对 `gocell scaffold assembly` 的完整执行（default deploy=k8s）断言：
//  1. 6 个文件全部落盘
//  2. assembly.yaml 包含 id / cells / owner 字段，不含 deployTemplate
//  3. modules_gen.go / main.go / boundary.yaml 含 gocell generated marker
//  4. run.go / app.go 含 package main 声明
//
// RED：scaffoldAssembly 尚未使用 PlanAssemblyScaffold，不会产出 modules_gen.go
// / main.go / boundary.yaml（这三个走 codegen.Write，不走 WritePlannedFiles，
// 因此 all-or-nothing rollback 无法保证）。
func TestScaffoldAssembly_GoldenSixFiles(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "goldcell")

	args := []string{
		"--id=goldasm",
		"--cells=goldcell",
		"--team=platform",
		"--role=maintainer",
		"--deploy=k8s",
	}
	if err := scaffoldAssembly(root, args); err != nil {
		t.Fatalf("scaffoldAssembly: %v", err)
	}

	// 1. 六文件全部落盘
	sixRels := []string{
		"assemblies/goldasm/assembly.yaml",
		"cmd/goldasm/run.go",
		"cmd/goldasm/app.go",
		"cmd/goldasm/modules_gen.go",
		"cmd/goldasm/main.go",
		"assemblies/goldasm/generated/boundary.yaml",
	}
	for _, rel := range sixRels {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("golden: file missing: %s", rel)
		}
	}

	// 2. assembly.yaml 内容
	asmContent := fileutil.MustReadFile(t, filepath.Join(root, "assemblies", "goldasm", "assembly.yaml"))
	asmGot := string(asmContent)
	for _, want := range []string{"id: goldasm", "goldcell", "platform", "maintainer"} {
		if !strings.Contains(asmGot, want) {
			t.Errorf("golden: assembly.yaml missing %q\ngot:\n%s", want, asmGot)
		}
	}
	if strings.Contains(asmGot, "deployTemplate") {
		t.Errorf("golden: assembly.yaml must not contain deployTemplate for k8s; got:\n%s", asmGot)
	}

	// 3. 派生文件含 gocell generated marker
	generatedRels := []string{
		"cmd/goldasm/modules_gen.go",
		"cmd/goldasm/main.go",
		"assemblies/goldasm/generated/boundary.yaml",
	}
	for _, rel := range generatedRels {
		content := fileutil.MustReadFile(t, filepath.Join(root, filepath.FromSlash(rel)))
		if !strings.HasPrefix(string(content), "// Code generated by gocell generate") &&
			!strings.HasPrefix(string(content), "# Generated by gocell generate") {
			t.Errorf("golden: %s must start with gocell generated marker; prefix=%q",
				rel, string(content[:min64(len(content))]))
		}
	}

	// 4. scaffold Go 文件含 package main
	for _, rel := range []string{"cmd/goldasm/run.go", "cmd/goldasm/app.go"} {
		content := fileutil.MustReadFile(t, filepath.Join(root, filepath.FromSlash(rel)))
		if !strings.Contains(string(content), "package main") {
			t.Errorf("golden: %s must contain 'package main'; got prefix=%q",
				rel, string(content[:min64(len(content))]))
		}
	}
}

func min64(n int) int {
	if n < 64 {
		return n
	}
	return 64
}
