package archtest

// scanner_framework_usage_test.go — guard archtest tools/archtest/*_test.go from
// reverting to raw filepath.WalkDir/filepath.Walk after PR #419 introduced the
// shared scanner framework at tools/archtest/internal/scanner/.
//
// Single-rule file per CLAUDE.md "新增 invariant 决策原则" file naming branch
// (single rule → {rule}_test.go). Promote to {theme}_invariants_test.go if
// related SCANNER-* invariants accumulate to ≥ 3.

import (
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
// tools/archtest/internal/scanner framework instead of raw filepath.WalkDir
// or filepath.Walk. The framework provides fail-closed scope predicates,
// structured diagnostic emission, built-in vendor/testdata/worktrees/generated/
// .git/node_modules skip, self-protection (auto-exclude scanner package itself),
// and uniform module-relative path display in error messages. Raw filepath
// walks bypass these guarantees and silently grow drift across rules.
//
// Scope: only top-level tools/archtest/*_test.go files (where archtest rules
// live). The scanner package itself (tools/archtest/internal/scanner/) is
// auto-excluded by Scope.collectFile's selfProtectRel; other internal
// subpackages (e.g. tools/archtest/internal/typeseval/) are unit tests for
// helpers and out of scope for this rule.
//
// Cannot funnel: the rule itself enforces consumer use of the funnel
// (the scanner framework), so it must be a hand-written archtest. type-system
// can't tell apart "framework-internal walk" (legitimate) from "consumer raw
// walk" (forbidden); both are *ast.SelectorExpr against path/filepath.
//
// Authorized escape hatch — io/fs.WalkDir(os.DirFS(...)) for non-Go content:
// the scanner framework's EachFile is .go-only (it parses Go ASTs). Rules
// that scan non-Go content (YAML, JSON schemas, .md docs, opaque testdata
// fixtures) are allowed to use io/fs.WalkDir(os.DirFS(root), ...) directly.
// This rule only matches path/filepath.Walk[Dir] selectors; it does not
// match io/fs.WalkDir, so the escape hatch is silent by construction.
// Approved sites at the time of writing (each scans non-Go content):
//   - listener_dx_test.go (.md doc enumeration)
//   - event_camelcase_test.go (payload.schema.json files via os.ReadDir)
//   - security_defaults_test.go (examples docker-compose YAML via os.ReadDir)
//   - span_record_error_redact_test.go (fixture YAML under testdata/)
//
// Coverage of the SelectorExpr scan:
//
// The ast.Inspect walk below matches every *ast.SelectorExpr — not just
// CallExpr.Fun selectors — so all of the following are caught at the
// definition site:
//   - direct call: filepath.WalkDir(root, fn)
//   - function-value binding: fp := filepath.WalkDir; fp(...)
//   - var declaration: var w = filepath.WalkDir
//   - argument passing: runWith(filepath.WalkDir)
//   - struct/slice literal: walkers := []func(...){filepath.WalkDir}
//
// The one bypass the SelectorExpr walk cannot see is dot-import:
//
//	import . "path/filepath"
//	WalkDir(root, fn)  // unqualified — no SelectorExpr at all
//
// PackageAliases excludes dot-imports (imports.go honors imp.Name == "."),
// so with a dot-import the loop below would early-return on len(aliases)==0.
// The dot-import detection added before the SelectorExpr scan closes this
// gap by flagging the import declaration directly.
//
// New rules scanning .go files MUST go through the scanner framework.
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

		// Dot-import bypass guard: `import . "path/filepath"` exposes WalkDir/Walk
		// as unqualified identifiers, which the SelectorExpr scan below cannot
		// see. Flag the import declaration unconditionally — there is no
		// legitimate reason to dot-import path/filepath in archtest tests
		// (filepath.ToSlash/Dir/Abs are clearer when qualified, and dot-imports
		// are golangci-lint dot-imports flagged elsewhere).
		for _, imp := range fc.File.Imports {
			if imp == nil || imp.Path == nil || imp.Path.Value != `"path/filepath"` {
				continue
			}
			if imp.Name == nil || imp.Name.Name != "." {
				continue
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    fc.Fset.Position(imp.Pos()).Line,
				Message: `dot-import of "path/filepath" forbidden in archtest tests; use named import + tools/archtest/internal/scanner for walks`,
			})
		}

		// Self-exempt: this archtest file legitimately needs path/filepath only
		// for filepath.ToSlash/Dir parsing of fc.Rel — not for walking. Detect
		// by absence of WalkDir/Walk SelectorExpr in this very file's AST.
		aliases := scanner.PackageAliases(fc.File, "path/filepath")
		if len(aliases) == 0 {
			return
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
			if _, isFilepathAlias := aliases[ident.Name]; !isFilepathAlias {
				return true
			}
			if sel.Sel.Name != "WalkDir" && sel.Sel.Name != "Walk" {
				return true
			}
			line := fc.Fset.Position(sel.Pos()).Line
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    line,
				Message: "use tools/archtest/internal/scanner (DirsScope/ModuleScope + EachFile) instead of filepath." + sel.Sel.Name,
			})
			return true
		})
	})
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-01", diags)
}

// TestScannerFrameworkUsage01_DotImportFixture proves the dot-import detection
// branch added alongside SCANNER-FRAMEWORK-USAGE-01 actually fires. Without
// this test, an AI refactor could silently drop the dot-import guard and the
// main rule would still pass (because none of the live archtest files
// dot-import path/filepath today). Inline-source fixture so no testdata file
// is needed.
func TestScannerFrameworkUsage01_DotImportFixture(t *testing.T) {
	t.Parallel()
	src := `package fake
import . "path/filepath"
func _() string { return Join("a", "b") }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var hits int
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil || imp.Path.Value != `"path/filepath"` {
			continue
		}
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		hits++
	}
	if hits != 1 {
		t.Fatalf("dot-import detection: expected 1 hit on fixture, got %d", hits)
	}

	// PackageAliases must continue to exclude dot-imports — that's why the
	// dot-import branch above is needed in the first place.
	aliases := scanner.PackageAliases(file, "path/filepath")
	if _, hasDot := aliases["."]; hasDot {
		t.Fatal("PackageAliases must exclude dot-imports; got '.' in alias set")
	}
	if len(aliases) != 0 {
		t.Fatalf("PackageAliases on dot-import-only file: expected 0 named aliases, got %d (%v)", len(aliases), aliases)
	}
}
