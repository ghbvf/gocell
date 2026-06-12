//go:build archtest

// INVARIANT: CONTRACT-OWNER-CELL-FUNNEL-01

// Package archtest — CONTRACT-OWNER-CELL-FUNNEL-01 guards the sealed
// ContractOwner funnel against bypass.
//
// kernel/metadata.ContractOwner (owner.go) is the sealed typed union resolving a
// contract's owner into Cell(id) | Framework. Its Cell() accessor returns
// ok=false for a framework owner, making "treat the framework as if it were a
// cell" unexpressible at the type level (the Hard core). This archtest is the
// Medium companion that keeps the funnel from being bypassed: it forbids
// indexing a `.Cells` map directly by a raw `.OwnerCell` selector
// (`project.Cells[c.OwnerCell]`) anywhere in kernel/governance. Such an index
// re-introduces the cell-existence assumption REF-03 used before the funnel —
// it would silently treat a framework-owned contract's "_framework" sentinel as
// a missing cell. Owner-cell-to-cell resolution MUST go through
// `c.Owner().Cell()` instead.
//
// Legitimate raw `.OwnerCell` reads remain allowed (equality comparison in
// CONTRACT-CONSISTENCY-EMIT-01, grouping in kernel/registry.ByOwner); only the
// dangerous Cells-index-by-OwnerCell shape is forbidden.
//
// AI-robust: Hard core (sealed ContractOwner type — forging a framework owner or
// extracting a cell from one is unexpressible) + this Medium anti-bypass scan
// (typed AST pattern with a synthetic red case below). No Soft.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cellsIndexedByOwnerCell walks the given files for the forbidden pattern
// `<expr>.Cells[<expr>.OwnerCell]` and returns one human-readable position
// string per occurrence.
func cellsIndexedByOwnerCell(fset *token.FileSet, files []*ast.File) []string {
	var hits []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ix, ok := n.(*ast.IndexExpr)
			if !ok {
				return true
			}
			if isSelectorNamed(ix.X, "Cells") && isSelectorNamed(ix.Index, "OwnerCell") {
				hits = append(hits, fset.Position(ix.Pos()).String())
			}
			return true
		})
	}
	return hits
}

// isSelectorNamed reports whether expr is a selector whose final segment is name
// (e.g. v.project.Cells → "Cells"; c.OwnerCell → "OwnerCell").
func isSelectorNamed(expr ast.Expr, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == name
}

// TestContractOwnerCellFunnel_NoBypass asserts kernel/governance never indexes a
// .Cells map directly by a raw .OwnerCell — owner→cell resolution must funnel
// through ContractOwner.Cell().
func TestContractOwnerCellFunnel_NoBypass(t *testing.T) {
	root := findModuleRoot(t)
	dir := filepath.Join(root, "kernel", "governance")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read kernel/governance: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("anti-vacuity: parsed 0 non-test files from kernel/governance")
	}

	if hits := cellsIndexedByOwnerCell(fset, files); len(hits) > 0 {
		t.Errorf("CONTRACT-OWNER-CELL-FUNNEL-01: %d forbidden `*.Cells[*.OwnerCell]` index(es) in kernel/governance;\n"+
			"resolve the owner via c.Owner().Cell() instead of indexing Cells by the raw OwnerCell string:\n  %s",
			len(hits), strings.Join(hits, "\n  "))
	}
}

// TestContractOwnerCellFunnel_DetectorIsNotVacuous proves the detector actually
// fires on the forbidden pattern (synthetic red case), so a green run on the
// real package means "pattern absent", not "detector broken".
func TestContractOwnerCellFunnel_DetectorIsNotVacuous(t *testing.T) {
	const red = `package p
type cells = map[string]int
type project struct{ Cells cells }
type validator struct{ project project }
type contract struct{ OwnerCell string }
func (v validator) bad(c contract) { _ = v.project.Cells[c.OwnerCell] }
`
	const green = `package p
type cells = map[string]int
type project struct{ Cells cells }
type validator struct{ project project }
type owner struct{}
func (owner) Cell() (string, bool) { return "", true }
type contract struct{ OwnerCell string }
func (contract) Owner() owner { return owner{} }
func (v validator) good(c contract) { id, _ := c.Owner().Cell(); _ = v.project.Cells[id] }
`
	fset := token.NewFileSet()
	redFile, err := parser.ParseFile(fset, "red.go", red, 0)
	if err != nil {
		t.Fatalf("parse red fixture: %v", err)
	}
	greenFile, err := parser.ParseFile(fset, "green.go", green, 0)
	if err != nil {
		t.Fatalf("parse green fixture: %v", err)
	}

	if hits := cellsIndexedByOwnerCell(fset, []*ast.File{redFile}); len(hits) != 1 {
		t.Errorf("detector must flag the synthetic bypass exactly once, got %d", len(hits))
	}
	if hits := cellsIndexedByOwnerCell(fset, []*ast.File{greenFile}); len(hits) != 0 {
		t.Errorf("detector must NOT flag the funnel form c.Owner().Cell(), got %d", len(hits))
	}
}
