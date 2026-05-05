package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		CellID:           "goldcell",
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
	content, err := os.ReadFile(cellGoPath) //nolint:gosec // test reads files it just wrote
	if err != nil {
		t.Fatalf("read cell.go: %v", err)
	}
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
		CellID:           "goldcell",
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
	content, err := os.ReadFile(cellYAMLPath) //nolint:gosec // test reads files it just wrote
	if err != nil {
		t.Fatalf("read cell.yaml: %v", err)
	}
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
