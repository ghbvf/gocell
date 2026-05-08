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
