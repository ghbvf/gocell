// INVARIANT: NO-MANUAL-CONTRACTSPEC-LITERAL-01
//
// NO-MANUAL-CONTRACTSPEC-LITERAL-01
//
// Invariant: wrapper.ContractSpec{…} composite literals and wrapper.EventSpec(…)
// call expressions must only appear in generated/contracts/**/*_gen.go files.
// Hand-written production code under cells/, examples/**/cells/, runtime/,
// kernel/cell/, adapters/ etc. must not define ContractSpec or EventSpec
// literals once the codegen migration (W3) is complete.
//
// Migration allowlist: the four cells still migrating in W3.2–W3.5 are
// listed in migrationAllowlistNoLiteral. Each sub-wave removes the
// corresponding entry. After W3.5 the list must be empty.
//
// Exclusions:
//   - generated/contracts/**/*_gen.go  — the authoritative home after migration
//   - tools/codegen/**/testdata/**     — codegen fixture files
//   - **/fixtures/**                   — test fixture trees
//   - kernel/wrapper/** itself         — defines the types (not instantiates them)
//   - *_test.go                        — test helpers may reference specs for assertions
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#PR4 W3
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// migrationAllowlistNoLiteral parallels migrationAllowlistCells. Must be empty
// after W3.5.
var migrationAllowlistNoLiteral = []string{}

// permanentPathExceptionsLiteral lists file paths (relative to repo root, forward-slash)
// that are permanently exempt from NO-MANUAL-CONTRACTSPEC-LITERAL-01.
// W3.5 complete: all accesscore slices use generated NewHandler; auth flags
// (Public/PasswordResetExempt) are declared in contract.yaml endpoints.http.auth
// and emitted by contractgen handler.tmpl — no cells/ file needs a manual
// wrapper.ContractSpec{} composite literal.
var permanentPathExceptionsLiteral = []string{}

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01 scans production .go files (excluding
// generated/, testdata, fixtures, kernel/wrapper) for wrapper.ContractSpec{…}
// composite literals and wrapper.EventSpec(…) call expressions, failing on any
// found outside the migration allowlist.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files := collectContractSpecScanFiles(t, root)

	var violations []string
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if isLiteralMigratingCell(rel) {
			continue
		}
		if isPermanentExceptionLiteral(rel) {
			continue
		}
		hits := scanForContractSpecLiterals(token.NewFileSet(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("NO-MANUAL-CONTRACTSPEC-LITERAL-01: %s", v)
	}
}

// isLiteralMigratingCell returns true when rel belongs to an allowlisted cell.
func isLiteralMigratingCell(rel string) bool {
	for _, cell := range migrationAllowlistNoLiteral {
		if strings.Contains(rel, "/"+cell+"/") || strings.HasSuffix(rel, "/"+cell) {
			return true
		}
	}
	return false
}

// isPermanentExceptionLiteral returns true when rel is in permanentPathExceptionsLiteral.
// W3.5 complete: this list is empty; all cells/ files use generated contract packages.
func isPermanentExceptionLiteral(rel string) bool {
	for _, exception := range permanentPathExceptionsLiteral {
		if rel == exception {
			return true
		}
	}
	return false
}

// collectContractSpecScanFiles returns production .go files to scan.
// Scope: cells (top-level cells/ + examples/*/cells/) only — runtime/ and
// kernel/ own framework-internal ContractSpec usages that are intentional
// and not subject to this migration gate. Cells discovered via
// findCellProductionGoFiles (metadata-driven). *_gen.go files are excluded
// here (gate is about hand-written cell code).
func collectContractSpecScanFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	filtered := files[:0]
	for _, f := range files {
		if !strings.HasSuffix(f, "_gen.go") {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture verifies that the
// scanner correctly identifies a wrapper.ContractSpec{} literal in a
// hand-written file. The fixture in testdata/no_manual_contractspec_literal/
// contains a deliberate violation.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "no_manual_contractspec_literal", "violates", "handler.go"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	// Simulate the relative path as it would appear under cells/.
	rel := "cells/fake/slices/bad/handler.go"
	violations := scanForContractSpecLiterals(token.NewFileSet(), fixturePath, rel)
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for fixture with manual ContractSpec literal, got 0")
	}
	// Verify the violation message is informative.
	for _, v := range violations {
		if !strings.Contains(v, "ContractSpec") {
			t.Errorf("violation message should mention ContractSpec: %q", v)
		}
	}
}

// scanForContractSpecLiterals AST-scans f for:
//  1. wrapper.ContractSpec{…} composite literals
//  2. wrapper.EventSpec(…) call expressions
//
// where "wrapper" is the local alias for kernel/wrapper.
func scanForContractSpecLiterals(fset *token.FileSet, path, rel string) []string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax errors handled by build
	}

	alias := wrapperLocalAlias(f)
	if alias == "" {
		return nil // file does not import kernel/wrapper
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			// Match wrapper.ContractSpec{…}
			sel, ok := node.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok2 := sel.X.(*ast.Ident)
			if !ok2 || ident.Name != alias || sel.Sel.Name != "ContractSpec" {
				return true
			}
			pos := fset.Position(node.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: manual %s.ContractSpec{} literal — must be in generated/contracts/**/*_gen.go only",
				rel, pos.Line, alias,
			))
		case *ast.CallExpr:
			// Match wrapper.EventSpec(…)
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok2 := sel.X.(*ast.Ident)
			if !ok2 || ident.Name != alias || sel.Sel.Name != "EventSpec" {
				return true
			}
			pos := fset.Position(node.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: manual %s.EventSpec() call — must be in generated/contracts/**/*_gen.go only",
				rel, pos.Line, alias,
			))
		}
		return true
	})
	return violations
}
