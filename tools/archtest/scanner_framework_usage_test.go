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
// primitive. Forbidden (package-level functions only):
//
//	path/filepath: WalkDir, Walk, Glob
//	os:            ReadDir
//	io/ioutil:     ReadDir   (deprecated but still callable)
//	io/fs:         WalkDir, Walk, Glob, ReadDir
//
// Use scanner.DirsScope/ModuleScope + EachFile (.go), EachContentFile
// (YAML/JSON/MD/SQL/...), MatchRels (glob-style filter), IncludeTestdata /
// IncludeGenerated (default-skipped dirs) instead.
//
// Known limit (tracked as backlog PR430-FU-USAGE-01-TYPE-AWARE):
// method calls on values whose type is *os.File / fs.FS / etc. are NOT
// detected — e.g. `f := os.Open("dir"); f.ReadDir(-1)` bypasses this rule
// because AST cannot determine the receiver type without go/types info.
// Type-aware upgrade is registered for trigger-on-incident.
//
// Coverage (all funneled through forbiddenWalkRefs):
//   - SelectorExpr scan walks every selector node, not just CallExpr.Fun, so
//     direct call / function-value binding (`fp := filepath.WalkDir; fp(...)`)
//     / var declaration (`var w = filepath.WalkDir`) / argument passing
//     (`runWith(filepath.WalkDir)`) / struct or slice literal — all caught at
//     the definition site.
//   - Dot-import scan flags `import . "<pkg>"` directly, since with a
//     dot-import the symbol is unqualified and no SelectorExpr exists.
//   - PackageAliases handles renamed imports (`import iofs "io/fs"`) for the
//     SelectorExpr branch.
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
			diags = append(diags, forbiddenWalkRefs(fc.File, fc.Fset, fc.Rel, importPath, forbiddenWalkSymbols[importPath])...)
		}
	})
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-01", diags)
}

// forbiddenWalkImports lists the import paths whose directory-traversal
// symbols are banned in archtest tests. Order is fixed so diagnostics are
// emitted deterministically.
var forbiddenWalkImports = []string{"path/filepath", "os", "io/ioutil", "io/fs"}

// forbiddenWalkSymbols maps each banned import path to the directory-traversal
// symbols that must not be used in archtest tests. Adding a new primitive
// (e.g. "embed.FS.ReadDir" via fs.ReadDirFS) means extending this table.
//
// Method calls (e.g. *os.File.ReadDir) are NOT in this table because AST
// cannot resolve receiver types — see PR430-FU-USAGE-01-TYPE-AWARE backlog.
var forbiddenWalkSymbols = map[string][]string{
	"path/filepath": {"WalkDir", "Walk", "Glob"},
	"os":            {"ReadDir"},
	"io/ioutil":     {"ReadDir"},
	"io/fs":         {"WalkDir", "Walk", "Glob", "ReadDir"},
}

// forbiddenWalkRefs returns the diagnostics for every banned reference to
// importPath/symbols in file. Two branches, both AST-only:
//
//  1. Dot-import: `import . "<importPath>"` exposes symbols as unqualified
//     identifiers; the SelectorExpr scan in (2) cannot see them. Flag the
//     import declaration directly.
//  2. SelectorExpr: walks every *ast.SelectorExpr (not just CallExpr.Fun) so
//     direct calls, function-value bindings, var declarations, argument
//     passing, struct/slice literals are all caught at the definition site.
//
// Used by both the live TestScannerFrameworkUsage01 and the
// TestScannerFrameworkUsage01_ForbiddenWalkFixture table-driven proof —
// fixture and rule cannot drift apart because they call the same function.
func forbiddenWalkRefs(file *ast.File, fset *token.FileSet, rel, importPath string, symbols []string) []scanner.Diagnostic {
	var out []scanner.Diagnostic

	// (1) Dot-import branch.
	target := `"` + importPath + `"`
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil || imp.Path.Value != target {
			continue
		}
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		out = append(out, scanner.Diagnostic{
			Rel:     rel,
			Line:    fset.Position(imp.Pos()).Line,
			Message: fmt.Sprintf(`dot-import of %q forbidden in archtest tests; use named import + tools/archtest/internal/scanner`, importPath),
		})
	}

	// (2) Aliased / direct SelectorExpr branch.
	aliases := scanner.PackageAliases(file, importPath)
	if len(aliases) == 0 {
		return out
	}
	symbolSet := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		symbolSet[s] = struct{}{}
	}
	ast.Inspect(file, func(n ast.Node) bool {
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
		out = append(out, scanner.Diagnostic{
			Rel:     rel,
			Line:    fset.Position(sel.Pos()).Line,
			Message: fmt.Sprintf("use tools/archtest/internal/scanner instead of %s.%s", importPath, sel.Sel.Name),
		})
		return true
	})
	return out
}

// TestScannerFrameworkUsage01_ForbiddenWalkFixture exercises forbiddenWalkRefs
// directly via parsed-from-string fixtures. The 10 cases enumerate every
// AST shape the live rule must catch, plus negatives. Because both the live
// rule and this fixture call forbiddenWalkRefs, they cannot drift: a
// refactor that weakens the rule would flip at least one fixture case red.
func TestScannerFrameworkUsage01_ForbiddenWalkFixture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		src        string
		importPath string
		wantHits   int // expected diagnostic count
	}{
		// --- Dot-import branch (3 cases × 3 banned import paths) ---
		{
			name: "dot_import_path_filepath",
			src: `package fake
import . "path/filepath"
func _() string { return Join("a", "b") }
`,
			importPath: "path/filepath",
			wantHits:   1,
		},
		{
			name: "dot_import_os",
			src: `package fake
import . "os"
func _() error { _, err := ReadDir("/tmp"); return err }
`,
			importPath: "os",
			wantHits:   1,
		},
		{
			name: "dot_import_io_fs",
			src: `package fake
import . "io/fs"
func _() error { return WalkDir(nil, ".", nil) }
`,
			importPath: "io/fs",
			wantHits:   1,
		},

		// --- SelectorExpr branch — 5 AST shapes that must all be caught ---
		{
			name: "direct_call",
			src: `package fake
import "path/filepath"
func _() error { return filepath.WalkDir(".", nil) }
`,
			importPath: "path/filepath",
			wantHits:   1,
		},
		{
			name: "function_value_binding",
			src: `package fake
import "path/filepath"
func _() { fp := filepath.WalkDir; _ = fp }
`,
			importPath: "path/filepath",
			wantHits:   1, // RHS SelectorExpr caught at binding site
		},
		{
			name: "var_declaration",
			src: `package fake
import "path/filepath"
import "io/fs"
var _ fs.WalkDirFunc
var _ = filepath.Walk
`,
			importPath: "path/filepath",
			wantHits:   1, // Walk in var init
		},
		{
			name: "argument_passing",
			src: `package fake
import "path/filepath"
func consume(any) {}
func _() { consume(filepath.WalkDir) }
`,
			importPath: "path/filepath",
			wantHits:   1, // SelectorExpr as call argument
		},
		{
			name: "struct_or_slice_literal",
			src: `package fake
import "path/filepath"
var _ = []any{filepath.WalkDir}
`,
			importPath: "path/filepath",
			wantHits:   1, // SelectorExpr inside composite literal
		},

		// --- ioutil.ReadDir (deprecated package, still callable) ---
		{
			name: "ioutil_readdir_call",
			src: `package fake
import "io/ioutil"
func _() error { _, err := ioutil.ReadDir("/tmp"); return err }
`,
			importPath: "io/ioutil",
			wantHits:   1,
		},

		// --- Alias propagation positive (the bypass concern from review) ---
		{
			name: "renamed_import_alias_call",
			src: `package fake
import iofs "io/fs"
func _() error { return iofs.WalkDir(nil, ".", nil) }
`,
			importPath: "io/fs",
			wantHits:   1, // PackageAliases finds "iofs"; SelectorExpr scan flags it
		},

		// --- Negatives (must NOT hit) ---
		{
			name: "named_import_non_banned_symbol",
			src: `package fake
import "path/filepath"
func _() string { return filepath.Join("a", "b") }
`,
			importPath: "path/filepath",
			wantHits:   0, // Join is not banned
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
			diags := forbiddenWalkRefs(file, fset, "fake.go", tc.importPath, forbiddenWalkSymbols[tc.importPath])
			if len(diags) != tc.wantHits {
				t.Errorf("forbiddenWalkRefs(%q): got %d hits, want %d (diags=%v)", tc.importPath, len(diags), tc.wantHits, diags)
			}
		})
	}
}

// TestScannerFrameworkUsage01_PackageAliasesExcludesDot pins the contract that
// scanner.PackageAliases excludes dot-imports — the existence of the dot-import
// branch in forbiddenWalkRefs depends on this. If a future scanner refactor
// includes "." in the alias set, the SelectorExpr scan would emit a diagnostic
// on every unqualified identifier and the dot-import branch would become
// redundant; we'd need to revisit both. This test catches that drift early.
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
