package archtest

// scanner_framework_usage_test.go — guard archtest tools/archtest/*_test.go
// from bypassing the shared scanner framework at tools/archtest/internal/scanner.
//
// Single-rule file per CLAUDE.md "新增 invariant 决策原则" file naming branch
// (single rule → {rule}_test.go). Promote to {theme}_invariants_test.go if
// related SCANNER-* invariants accumulate to ≥ 3.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// INVARIANT: SCANNER-FRAMEWORK-USAGE-01
//
// archtest *_test.go files at tools/archtest/<file>_test.go must use the
// tools/archtest/internal/scanner framework instead of any directory-traversal
// primitive. Forbidden:
//
//	path/filepath: WalkDir, Walk, Glob
//	os:            ReadDir
//	io/fs:         WalkDir, Walk, Glob, ReadDir
//
// Use scanner.DirsScope/ModuleScope + EachFile (.go), EachContentFile
// (YAML/JSON/MD/SQL/...), MatchRels (glob-style filter), IncludeTestdata
// (fixtures under testdata/) instead.
//
// Coverage:
//   - SelectorExpr scan walks every selector node, not just CallExpr.Fun, so
//     direct call / function-value binding (`fp := filepath.WalkDir; fp(...)`)
//     / var declaration (`var w = filepath.WalkDir`) / argument passing
//     (`runWith(filepath.WalkDir)`) / struct or slice literal — all caught at
//     the definition site.
//   - Dot-import scan (dotImportForbidden) flags `import . "<pkg>"` directly,
//     since with a dot-import the symbol is unqualified and no SelectorExpr
//     exists.
//   - PackageAliases handles renamed imports (`import iofs "io/fs"`).
//
// Cannot funnel: the rule itself enforces consumer use of the funnel; type
// system can't tell apart "framework-internal walk" (legitimate) from
// "consumer raw walk" (forbidden).
//
// New rules MUST go through the scanner framework.
func TestScannerFrameworkUsage01(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"tools/archtest"}, scanner.IncludeTests())

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Only flag top-level archtest test files (tools/archtest/<file>_test.go).
		// Subpackages under tools/archtest/internal/ are out of scope.
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.AbsPath, "_test.go") {
			return
		}

		for _, importPath := range forbiddenWalkImports {
			symbols := forbiddenWalkSymbols[importPath]

			// Dot-import branch.
			if pos, ok := dotImportForbidden(fc.File, importPath); ok {
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    fc.Fset.Position(pos).Line,
					Message: fmt.Sprintf(`dot-import of %q forbidden in archtest tests; use named import + tools/archtest/internal/scanner`, importPath),
				})
			}

			// Aliased-import branch.
			aliases := scanner.PackageAliases(fc.File, importPath)
			if len(aliases) == 0 {
				continue
			}
			symbolSet := make(map[string]struct{}, len(symbols))
			for _, s := range symbols {
				symbolSet[s] = struct{}{}
			}
			ast.Inspect(fc.File, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, isAlias := aliases[ident.Name]; !isAlias {
					return true
				}
				if _, banned := symbolSet[sel.Sel.Name]; !banned {
					return true
				}
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    fc.Fset.Position(sel.Pos()).Line,
					Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of %s.%s", importPath, sel.Sel.Name),
				})
				return true
			})
		}
	})
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-01", diags)
}

// forbiddenWalkImports lists the import paths whose directory-traversal
// symbols are banned in archtest tests. Order is fixed so diagnostics are
// emitted deterministically.
var forbiddenWalkImports = []string{"path/filepath", "os", "io/fs"}

// forbiddenWalkSymbols maps each banned import path to the directory-traversal
// symbols that must not be used in archtest tests. Adding a new primitive
// (e.g. "embed.FS.ReadDir" via fs.ReadDirFS) means extending this table.
var forbiddenWalkSymbols = map[string][]string{
	"path/filepath": {"WalkDir", "Walk", "Glob"},
	"os":            {"ReadDir"},
	"io/fs":         {"WalkDir", "Walk", "Glob", "ReadDir"},
}

// dotImportForbidden reports whether file dot-imports importPath. When found,
// it returns the position of the import declaration so callers can emit a
// diagnostic with the correct line. PackageAliases excludes dot-imports
// (imports.go honors imp.Name == "."), so without this dedicated check the
// SelectorExpr scan would early-return on len(aliases)==0 and miss the file.
func dotImportForbidden(file *ast.File, importPath string) (token.Pos, bool) {
	target := `"` + importPath + `"`
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil {
			continue
		}
		if imp.Path.Value != target {
			continue
		}
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		return imp.Pos(), true
	}
	return token.NoPos, false
}

// TestScannerFrameworkUsage01_DotImportFixture proves dotImportForbidden
// behaves correctly on hand-crafted fixtures for every banned import path.
// AI refactors that silently drop a case from forbiddenWalkImports, or weaken
// the helper's import-path comparison, would flip a fixture case to red.
func TestScannerFrameworkUsage01_DotImportFixture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		src        string
		importPath string
		wantHit    bool
	}{
		{
			name: "dot_import_path_filepath",
			src: `package fake
import . "path/filepath"
func _() string { return Join("a", "b") }
`,
			importPath: "path/filepath",
			wantHit:    true,
		},
		{
			name: "dot_import_os",
			src: `package fake
import . "os"
func _() error { _, err := ReadDir("/tmp"); return err }
`,
			importPath: "os",
			wantHit:    true,
		},
		{
			name: "dot_import_io_fs",
			src: `package fake
import . "io/fs"
func _() error { return WalkDir(nil, ".", nil) }
`,
			importPath: "io/fs",
			wantHit:    true,
		},
		{
			name: "named_import_no_dot_filepath",
			src: `package fake
import "path/filepath"
func _() string { return filepath.Join("a", "b") }
`,
			importPath: "path/filepath",
			wantHit:    false,
		},
		{
			name: "different_package_dot_import",
			src: `package fake
import . "strings"
func _() string { return Join([]string{}, "/") }
`,
			importPath: "path/filepath",
			wantHit:    false,
		},
		{
			name: "renamed_import_not_dot",
			src: `package fake
import iofs "io/fs"
func _() error { return iofs.WalkDir(nil, ".", nil) }
`,
			importPath: "io/fs",
			wantHit:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fake.go", tc.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, found := dotImportForbidden(file, tc.importPath)
			if found != tc.wantHit {
				t.Errorf("dotImportForbidden(file, %q): got %v, want %v", tc.importPath, found, tc.wantHit)
			}
		})
	}
}

// TestScannerFrameworkUsage01_PackageAliasesExcludesDot pins the contract that
// scanner.PackageAliases excludes dot-imports — the existence of the
// dotImportForbidden branch in TestScannerFrameworkUsage01 depends on this.
// If a future scanner refactor includes "." in the alias set, the SelectorExpr
// scan would emit a diagnostic on every unqualified identifier and the
// dot-import branch would become redundant; we'd need to revisit both. This
// test catches that drift early.
func TestScannerFrameworkUsage01_PackageAliasesExcludesDot(t *testing.T) {
	t.Parallel()
	src := `package fake
import . "path/filepath"
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	aliases := scanner.PackageAliases(file, "path/filepath")
	if _, hasDot := aliases["."]; hasDot {
		t.Fatal("PackageAliases must exclude dot-imports; got '.' in alias set")
	}
	if len(aliases) != 0 {
		t.Fatalf("PackageAliases on dot-import-only file: expected 0 named aliases, got %d (%v)", len(aliases), aliases)
	}
}
