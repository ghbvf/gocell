// invariants asserted in this file:
//   - INVARIANT: CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01
//
// Package archtest — cellgen scaffold errcode funnel invariant.
//
// CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01: scaffold.go / scaffold_bundle.go in
// tools/codegen/cellgen/ must not call fmt.Errorf. All errors must funnel
// through pkg/errcode (errcode.New / errcode.Wrap) so message PII-safe
// constraints apply uniformly (CLAUDE.md error-handling rules).
//
// AI-rebust: Medium (scanner AST + concrete-package allowlist). Hard
// upgrade path tracked under backlog CELLGEN-ERRCODE-FUNNEL-HARDEN (trigger:
// ad-hoc fmt.Errorf reintroduced; requires method-level depguard or typed
// Error return wrapper, Cx3-Cx4 work).
package archtest

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestCellgenScaffoldErrcodeFunnel enforces CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01:
// scaffold.go and scaffold_bundle.go in tools/codegen/cellgen/ must not call
// fmt.Errorf. All errors must go through pkg/errcode (errcode.New / errcode.Wrap)
// so that MESSAGE-CONST-LITERAL-01 and PII-safe constraints apply uniformly.
//
// Scanned files:
//   - tools/codegen/cellgen/scaffold.go
//   - tools/codegen/cellgen/scaffold_bundle.go
//
// Test files (*_test.go) are explicitly excluded.
func TestCellgenScaffoldErrcodeFunnel(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	scaffoldOnlyPred := scanner.MatchRels(func(rel string) bool {
		base := filepath.Base(rel)
		if strings.HasSuffix(base, "_test.go") {
			return false
		}
		// Only scaffold.go and scaffold_bundle.go are in scope.
		return base == "scaffold.go" || base == "scaffold_bundle.go"
	})

	scope := scanner.DirsScope(repoRoot, []string{
		"tools/codegen/cellgen",
	}, scaffoldOnlyPred)

	var violations []string

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		ast.Inspect(fc.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "fmt" && sel.Sel.Name == "Errorf" {
				pos := fc.Fset.Position(call.Lparen)
				violations = append(violations,
					fc.Rel+":"+strconv.Itoa(pos.Line)+": fmt.Errorf(...)")
			}
			return true
		})
	})

	if len(violations) > 0 {
		t.Fatalf("CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01: fmt.Errorf calls found in cellgen scaffold files.\n"+
			"All errors must go through pkg/errcode (errcode.New / errcode.Wrap).\n"+
			"Violations (%d):\n  %s", len(violations), strings.Join(violations, "\n  "))
	}
}
