//go:build archtest

// INVARIANT: CONTRACT-OWNER-CELL-FUNNEL-01

// Package archtest — CONTRACT-OWNER-CELL-FUNNEL-01 guards the sealed
// ContractOwner funnel against bypass.
//
// kernel/metadata.ContractOwner (owner.go) is the sealed typed union resolving a
// contract's owner into Cell(id) | Framework. Its Cell() accessor returns
// ok=false for a framework owner, making "treat the framework as if it were a
// cell" unexpressible at the type level (the Hard core). This archtest is the
// Medium companion that keeps the funnel from being bypassed in two shapes: the
// direct selector `project.Cells[c.OwnerCell]`, and the single-hop local alias
// `owner := c.OwnerCell; ... project.Cells[owner]` (intra-function, name-based
// taint), anywhere in kernel/governance. Either shape re-introduces the
// cell-existence assumption REF-03 used before the funnel — it would silently
// treat a framework-owned contract's "_framework" sentinel as a missing cell.
// Owner-cell-to-cell resolution MUST go through `c.Owner().Cell()` instead.
//
// Coverage and residual blind spot: the scan catches the direct shape and the
// single-hop local alias. It deliberately does NOT chase multi-hop aliases,
// cross-function flow, or map-variable aliasing (`m := proj.Cells; m[owner]`) —
// catching those would require full go/types dataflow, out of proportion for an
// AST guard. The raw `OwnerCell` string field stays readable by construction
// (this scan polices its dangerous USE, it does not seal the field), so those
// contrived shapes remain a blind spot of THIS scan. The Hard guarantee is the
// narrower, decisive one: code that resolves an owner through the sanctioned
// `Owner().Cell()` funnel can never mistake a framework owner for a cell, and
// that funnel is the one path all current kernel/governance code uses.
//
// Legitimate raw `.OwnerCell` reads remain allowed (equality comparison in
// CONTRACT-CONSISTENCY-EMIT-01, grouping in kernel/registry.ByOwner); only the
// dangerous Cells-index-by-OwnerCell shape (direct or single-hop alias) is
// forbidden.
//
// AI-robust: Hard core (sealed ContractOwner type — forging a framework owner or
// extracting a cell from one is unexpressible) + this Medium anti-bypass scan
// (typed AST pattern covering the direct + single-hop-alias shapes, with
// synthetic red cases below). No Soft.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// cellsIndexedByOwnerCell walks the given files for the forbidden cell-existence
// bypass — indexing a `.Cells` map by the raw owner string — in two shapes, and
// returns one human-readable position string per occurrence:
//
//   - direct selector: `<expr>.Cells[<expr>.OwnerCell]` (file-wide);
//   - single-hop local alias: `owner := c.OwnerCell; ... <expr>.Cells[owner]`
//     where the index ident was assigned from a `.OwnerCell` selector earlier in
//     the same function (per-function, name-based taint).
//
// The two passes match disjoint index node types (a SelectorExpr vs an Ident),
// so an index is counted at most once.
func cellsIndexedByOwnerCell(fset *token.FileSet, files []*ast.File) []string {
	var hits []string
	for _, f := range files {
		// Pass 1 (file-wide): the direct shape `<expr>.Cells[<expr>.OwnerCell]`.
		EachInSubtree[ast.IndexExpr](f, func(ix *ast.IndexExpr) {
			if isSelectorNamed(ix.X, "Cells") && isSelectorNamed(ix.Index, "OwnerCell") {
				hits = append(hits, fset.Position(ix.Pos()).String())
			}
		})
		// Pass 2 (per-function): the single-hop local-alias shape. Taint is
		// collected per function so an alias name cannot leak across boundaries.
		EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
			if fn.Body == nil {
				return
			}
			taint := ownerCellTaintedIdents(fn)
			if len(taint) == 0 {
				return
			}
			EachInSubtree[ast.IndexExpr](fn, func(ix *ast.IndexExpr) {
				id, ok := ix.Index.(*ast.Ident)
				if ok && isSelectorNamed(ix.X, "Cells") && taint[id.Name] {
					hits = append(hits, fset.Position(ix.Pos()).String())
				}
			})
		})
	}
	return hits
}

// ownerCellTaintedIdents returns the set of identifier names within fn that were
// assigned (`:=`, `=`, or `var x = ...`) directly from a `.OwnerCell` selector —
// e.g. `owner := c.OwnerCell`. Such an alias holds the raw owner string and is as
// dangerous as `.OwnerCell` itself when later used to index `.Cells`. This is a
// single-hop, name-based, intra-function taint set: it deliberately does NOT
// chase multi-hop aliases, cross-function flow, or map-variable aliasing — those
// residual bypasses are out of scope for this AST guard (see the package doc).
func ownerCellTaintedIdents(fn *ast.FuncDecl) map[string]bool {
	taint := map[string]bool{}
	EachInSubtree[ast.AssignStmt](fn, func(s *ast.AssignStmt) {
		for i, rhs := range s.Rhs {
			if i < len(s.Lhs) && isSelectorNamed(rhs, "OwnerCell") {
				if id, ok := s.Lhs[i].(*ast.Ident); ok {
					taint[id.Name] = true
				}
			}
		}
	})
	EachInSubtree[ast.ValueSpec](fn, func(s *ast.ValueSpec) {
		for i, val := range s.Values {
			if i < len(s.Names) && isSelectorNamed(val, "OwnerCell") {
				taint[s.Names[i].Name] = true
			}
		}
	})
	return taint
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
	paths, err := DirsScope(root, []string{"framework/kernel/governance"}).Files()
	if err != nil {
		t.Fatalf("read kernel/governance: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("anti-vacuity: parsed 0 non-test files from kernel/governance")
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		files = append(files, f)
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

// TestContractOwnerCellFunnel_DetectsLocalAlias proves the detector also catches
// the single-hop local-alias bypass — `owner := c.OwnerCell; ... Cells[owner]` —
// not just the direct `Cells[c.OwnerCell]` selector. An alias carries the same
// raw owner string and re-introduces the same cell-existence assumption, yet
// reads false-green to a selector-only scan. The anti-false-positive case proves
// an ident NOT derived from .OwnerCell is left alone even when it indexes .Cells.
func TestContractOwnerCellFunnel_DetectsLocalAlias(t *testing.T) {
	const aliasRed = `package p
type cells = map[string]int
type project struct{ Cells cells }
type validator struct{ project project }
type contract struct{ OwnerCell string }
func (v validator) bad(c contract) { owner := c.OwnerCell; _ = v.project.Cells[owner] }
`
	const aliasGreen = `package p
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
	redFile, err := parser.ParseFile(fset, "alias_red.go", aliasRed, 0)
	if err != nil {
		t.Fatalf("parse alias red fixture: %v", err)
	}
	greenFile, err := parser.ParseFile(fset, "alias_green.go", aliasGreen, 0)
	if err != nil {
		t.Fatalf("parse alias green fixture: %v", err)
	}

	if hits := cellsIndexedByOwnerCell(fset, []*ast.File{redFile}); len(hits) != 1 {
		t.Errorf("detector must flag the local-alias bypass exactly once, got %d", len(hits))
	}
	if hits := cellsIndexedByOwnerCell(fset, []*ast.File{greenFile}); len(hits) != 0 {
		t.Errorf("detector must NOT flag an ident derived from Owner().Cell(), got %d", len(hits))
	}
}
