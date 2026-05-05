// CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01
//
// Invariant: non-generated .go files under cells/ and examples/**/cells/
// must not import "kernel/wrapper" AND reference ContractSpec or EventSpec
// from that package. After the codegen migration (W3), all wrapper.ContractSpec
// and wrapper.EventSpec literals live exclusively in generated/contracts/**/*_gen.go
// (guarded by NO-MANUAL-CONTRACTSPEC-LITERAL-01).
//
// Migration allowlist: cells listed in migrationAllowlistCells below are exempt
// while sub-waves W3.2–W3.5 are in progress. Each sub-wave removes the
// corresponding entry. The list must be empty after W3.5.
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

// migrationAllowlistCells lists cell directory-name segments that are still
// migrating. A file path is exempt when any of these strings appears as a
// slash-delimited path segment within the file's relative path.
// Must be empty after W3.5.
var migrationAllowlistCells = []string{}

// permanentPathExceptionsCells lists file paths (relative to repo root, forward-slash)
// that are permanently exempt from CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01.
// W3.5 complete: all accesscore slices use generated NewHandler; auth flags
// (Public/PasswordResetExempt) are declared in contract.yaml endpoints.http.auth
// and emitted by contractgen handler.tmpl — no cells/ file needs wrapper.ContractSpec.
var permanentPathExceptionsCells = []string{}

const wrapperPkgSuffix = "/kernel/wrapper"

// TestCELLS_NO_WRAPPER_CONTRACTSPEC_IMPORT_01 walks all non-generated,
// non-test .go files under cells/ and examples/**/cells/ and fails when a
// file imports kernel/wrapper and references wrapper.ContractSpec or
// wrapper.EventSpec — unless the owning cell is in migrationAllowlistCells.
func TestCELLS_NO_WRAPPER_CONTRACTSPEC_IMPORT_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files := collectCellProductionGoFiles(t, root)

	var violations []string
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if isMigratingCell(rel) {
			continue
		}
		if isPermanentExceptionCell(rel) {
			continue
		}
		hits := scanForWrapperSpecUsage(fset_new(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01: %s", v)
	}
}

// isMigratingCell returns true when rel belongs to a cell in the allowlist.
// Matches "/cellName/" as an interior segment or "/cellName" as a suffix.
func isMigratingCell(rel string) bool {
	for _, cell := range migrationAllowlistCells {
		if strings.Contains(rel, "/"+cell+"/") || strings.HasSuffix(rel, "/"+cell) {
			return true
		}
	}
	return false
}

// isPermanentExceptionCell returns true when rel is in permanentPathExceptionsCells.
// W3.5 complete: this list is empty; all cells/ files use generated contract packages.
func isPermanentExceptionCell(rel string) bool {
	for _, exception := range permanentPathExceptionsCells {
		if rel == exception {
			return true
		}
	}
	return false
}

// collectCellProductionGoFiles returns all non-generated, non-test .go files
// under cells/ and examples/**/cells/.
func collectCellProductionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	roots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}
	for _, scanRoot := range roots {
		if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
			continue
		}
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // walk continues past unreadable entries
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
				return nil
			}
			files = append(files, path)
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// fset_new returns a fresh token.FileSet. Named to avoid shadowing the
// package-level fset in other test files.
func fset_new() *token.FileSet { return token.NewFileSet() }

// scanForWrapperSpecUsage returns violation strings when the file at path
// imports kernel/wrapper and references ContractSpec or EventSpec.
func scanForWrapperSpecUsage(fset *token.FileSet, path, rel string) []string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax error handled by build-test
	}

	alias := wrapperLocalAlias(f)
	if alias == "" {
		return nil // file does not import kernel/wrapper
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias {
			return true
		}
		switch sel.Sel.Name {
		case "ContractSpec", "EventSpec":
			pos := fset.Position(sel.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: uses %s.%s — migrate to generated contract package (see W3 plan)",
				rel, pos.Line, alias, sel.Sel.Name,
			))
		}
		return true
	})
	// Deduplicate (same expression may appear twice in AST traversal at different node kinds).
	seen := make(map[string]bool, len(violations))
	out := violations[:0]
	for _, v := range violations {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// wrapperLocalAlias returns the local identifier name for kernel/wrapper in f,
// or "" when not imported. Handles explicit aliases and the default last-segment name.
func wrapperLocalAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		imported := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasSuffix(imported, wrapperPkgSuffix) {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		parts := strings.Split(imported, "/")
		return parts[len(parts)-1]
	}
	return ""
}
