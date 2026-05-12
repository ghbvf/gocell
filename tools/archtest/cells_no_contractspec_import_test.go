// INVARIANT: CELLS-NO-CONTRACTSPEC-IMPORT-01
//
// # CELLS-NO-CONTRACTSPEC-IMPORT-01
//
// Invariant: non-generated .go files under cells/ and examples/**/cells/
// must not import "kernel/contractspec" AND reference ContractSpec from
// that package. After the codegen migration (W3) and the contractspec leaf
// extraction (G-04), all ContractSpec literals live exclusively in
// generated/contracts/**/*_gen.go (guarded by NO-MANUAL-CONTRACTSPEC-LITERAL-01).
//
// History: this rule was previously named CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01
// while ContractSpec lived in kernel/wrapper. After G-04 extracted ContractSpec
// to its own kernel/contractspec leaf, the rule moved with the type — the
// invariant ("cells/ must not directly construct ContractSpec literals") is
// the same; only the import-path anchor changed.
//
// Migration allowlist: cells listed in migrationAllowlistCells below are exempt
// while sub-waves W3.2–W3.5 are in progress. Each sub-wave removes the
// corresponding entry. The list must be empty after W3.5.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#PR4 W3 + G-04
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

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// migrationAllowlistCells lists cell directory-name segments that are still
// migrating. A file path is exempt when any of these strings appears as a
// slash-delimited path segment within the file's relative path.
// Must be empty after W3.5.
var migrationAllowlistCells = []string{}

// permanentPathExceptionsCells lists file paths (relative to repo root, forward-slash)
// that are permanently exempt from CELLS-NO-CONTRACTSPEC-IMPORT-01.
// W3.5 complete: all accesscore slices use generated NewHandler; auth flags
// (Public/PasswordResetExempt) are declared in contract.yaml endpoints.http.auth
// and emitted by contractgen handler.tmpl — no cells/ file needs contractspec.ContractSpec.
var permanentPathExceptionsCells = []string{}

const contractspecPkgSuffix = "/kernel/contractspec"

// TestCELLS_NO_CONTRACTSPEC_IMPORT_01 walks all non-generated, non-test .go
// files under cells/ and examples/**/cells/ and fails when a file imports
// kernel/contractspec and references contractspec.ContractSpec — unless the
// owning cell is in migrationAllowlistCells.
func TestCELLS_NO_CONTRACTSPEC_IMPORT_01(t *testing.T) {
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
		hits := scanForContractspecUsage(fset_new(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CELLS-NO-CONTRACTSPEC-IMPORT-01: %s", v)
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
// for every cell registered in ProjectMeta.Cells (covers top-level cells/
// and examples/*/cells/ via metadata path-pattern matching).
func collectCellProductionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	// Drop *_gen.go which findCellProductionGoFiles keeps (it only excludes
	// _test.go) — this gate is about hand-written cell code.
	filtered := files[:0]
	for _, f := range files {
		if !strings.HasSuffix(f, "_gen.go") {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// fset_new returns a fresh token.FileSet. Named to avoid shadowing the
// package-level fset in other test files.
func fset_new() *token.FileSet { return token.NewFileSet() }

// scanForContractspecUsage returns violation strings when the file at path
// imports kernel/contractspec and references ContractSpec.
func scanForContractspecUsage(fset *token.FileSet, path, rel string) []string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax error handled by build-test
	}

	alias := contractspecLocalAlias(f)
	if alias == "" {
		return nil // file does not import kernel/contractspec
	}

	// blockedNames covers the ContractSpec type itself and the typed funnels
	// (NewFrameworkHTTP / NewEventDerivation). The funnels are for runtime/
	// framework infra only — cells/ must use generated NewSubscription /
	// NewHandler adapters from generated/contracts/**.
	blockedNames := map[string]bool{
		"ContractSpec":       true,
		"NewFrameworkHTTP":   true,
		"NewEventDerivation": true,
	}

	var violations []string
	scanner.EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias {
			return
		}
		if blockedNames[sel.Sel.Name] {
			pos := fset.Position(sel.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: uses %s.%s — kernel/contractspec is for generated contracts "+
					"(cells/) and runtime/ framework infra only; cells/ must use "+
					"generated NewSubscription / NewHandler adapters",
				rel, pos.Line, alias, sel.Sel.Name,
			))
		}
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

// contractspecLocalAlias returns the local identifier name for kernel/contractspec
// in f, or "" when not imported. Handles explicit aliases and the default
// last-segment name.
func contractspecLocalAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		imported := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasSuffix(imported, contractspecPkgSuffix) {
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

// TestCELLS_NO_CONTRACTSPEC_IMPORT_01_NegativeFixture verifies that the
// scanner correctly identifies a file that imports kernel/contractspec and
// references contractspec.ContractSpec. The fixture in
// testdata/cells_no_contractspec/ contains a deliberate violation.
func TestCELLS_NO_CONTRACTSPEC_IMPORT_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "cells_no_contractspec", "violates", "handler.go"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	rel := "cells/fake/violates/handler.go"
	violations := scanForContractspecUsage(fset_new(), fixturePath, rel)
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for fixture with contractspec.ContractSpec reference, got 0")
	}
	for _, v := range violations {
		if !strings.Contains(v, "ContractSpec") {
			t.Errorf("violation message should mention ContractSpec: %q", v)
		}
	}
}

// TestCELLS_NO_CONTRACTSPEC_IMPORT_01_FunnelBlocked verifies that a cells/ file
// calling contractspec.NewFrameworkHTTP produces a violation. The typed funnels
// are for runtime/ framework infra only; cells/ must use generated adapters.
func TestCELLS_NO_CONTRACTSPEC_IMPORT_01_FunnelBlocked(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/kernel/contractspec"
func init() {
	_ = contractspec.NewFrameworkHTTP("http.fake.v1", "GET", "/api/v1/fake")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handler.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "funnel_test_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}

	alias := contractspecLocalAlias(f)
	if alias != "contractspec" {
		t.Fatalf("expected alias %q, got %q", "contractspec", alias)
	}

	violations := scanForContractspecUsage(token.NewFileSet(), tmp.Name(), "cells/fake/handler.go")
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for cells/ file calling contractspec.NewFrameworkHTTP, got 0")
	}
	for _, v := range violations {
		if !strings.Contains(v, "NewFrameworkHTTP") {
			t.Errorf("violation message should mention NewFrameworkHTTP: %q", v)
		}
	}
}

// TestBlankImportNoViolation explicitly locks the contract that a blank import
// of kernel/contractspec never triggers CELLS-NO-CONTRACTSPEC-IMPORT-01.
//
// In production Go, `import _ "..."` prevents any selector expression of the
// form `_.ContractSpec` (the blank identifier cannot be used as a qualifier).
// This test verifies that scanForContractspecUsage correctly produces zero
// violations when the local alias is "_", even when the source file's text
// contains the substring "_.ContractSpec" in a comment.
func TestBlankImportNoViolation(t *testing.T) {
	t.Parallel()
	// The only legal Go file with a blank import of contractspec cannot
	// reference _.ContractSpec as an expression; any occurrence in a comment
	// must not trigger the scanner (which operates on the AST, not raw text).
	src := `package p
// _.ContractSpec is mentioned here only as documentation.
import _ "github.com/ghbvf/gocell/kernel/contractspec"
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "blank_import.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Confirm alias is "_".
	alias := contractspecLocalAlias(f)
	if alias != "_" {
		t.Fatalf("expected alias \"_\", got %q", alias)
	}

	// Write to a temp file so scanForContractspecUsage can os.ReadFile it.
	tmp, err := os.CreateTemp(t.TempDir(), "blank_import_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}

	violations := scanForContractspecUsage(token.NewFileSet(), tmp.Name(), "cells/fake/blank_import.go")
	if len(violations) != 0 {
		t.Errorf("blank-import alias \"_\" must produce 0 violations; got %d: %v", len(violations), violations)
	}
}

// TestContractspecLocalAlias_TableDriven verifies the four key import patterns
// for kernel/contractspec: (a) no import, (b) default "contractspec" name,
// (c) explicit alias, (d) blank/underscore alias (import side effect — not
// used as identifier).
func TestContractspecLocalAlias_TableDriven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no_import",
			src:  `package p; import "fmt"; var _ = fmt.Sprintf`,
			want: "",
		},
		{
			name: "default_name",
			src:  `package p; import "github.com/ghbvf/gocell/kernel/contractspec"; var _ = contractspec.ContractSpec{}`,
			want: "contractspec",
		},
		{
			name: "explicit_alias_cs",
			src:  `package p; import cs "github.com/ghbvf/gocell/kernel/contractspec"; var _ = cs.ContractSpec{}`,
			want: "cs",
		},
		{
			name: "underscore_blank",
			src:  `package p; import _ "github.com/ghbvf/gocell/kernel/contractspec"`,
			want: "_",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "test.go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := contractspecLocalAlias(f)
			if got != tc.want {
				t.Errorf("contractspecLocalAlias = %q, want %q", got, tc.want)
			}
		})
	}
}
