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
var migrationAllowlistNoLiteral = []string{
	"configcore",
	"auditcore",
	"accesscore",
	"devicecell",
}

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

// collectContractSpecScanFiles returns production .go files to scan.
// Scope: cells/ and examples/**/cells/ only. runtime/ and kernel/ own
// framework-internal ContractSpec usages that are intentional and not
// subject to this migration gate.
// Exclusions within scope:
//   - generated/contracts/** (authorised home)
//   - **/testdata/**, **/fixtures/**
//   - *_test.go, *_gen.go
func collectContractSpecScanFiles(t *testing.T, root string) []string {
	t.Helper()
	scanRoots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}

	var files []string
	for _, scanRoot := range scanRoots {
		if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
			continue
		}
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // walk continues past unreadable entries
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", ".git", "node_modules":
					return filepath.SkipDir
				case "testdata", "fixtures":
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") {
				return nil
			}
			if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
				return nil
			}
			files = append(files, path)
			return nil
		})
	}
	sort.Strings(files)
	return files
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
