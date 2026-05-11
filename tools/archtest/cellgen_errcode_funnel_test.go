// invariants asserted in this file:
//   - INVARIANT: CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01
//
// Package archtest — cellgen errcode funnel invariant.
//
// CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01: all non-test .go files in
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
// all non-test .go files in tools/codegen/cellgen/ must not call fmt.Errorf.
// All errors must go through pkg/errcode (errcode.New / errcode.Wrap) so that
// MESSAGE-CONST-LITERAL-01 and PII-safe constraints apply uniformly.
//
// Scanned files:
//   - tools/codegen/cellgen/*.go (excluding *_test.go)
//
// Test files (*_test.go) are explicitly excluded.
func TestCellgenScaffoldErrcodeFunnel(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	cellgenAllPred := scanner.MatchRels(func(rel string) bool {
		base := filepath.Base(rel)
		// Exclude test files.
		if strings.HasSuffix(base, "_test.go") {
			return false
		}
		// Include all non-test .go files in tools/codegen/cellgen/.
		return strings.HasSuffix(base, ".go")
	})

	scope := scanner.DirsScope(repoRoot, []string{
		"tools/codegen/cellgen",
	}, cellgenAllPred)

	var violations []string

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		scanner.EachInSubtree[ast.CallExpr](fc.File, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return
			}
			if ident.Name == "fmt" && sel.Sel.Name == "Errorf" {
				pos := fc.Fset.Position(call.Lparen)
				violations = append(violations,
					fc.Rel+":"+strconv.Itoa(pos.Line)+": fmt.Errorf(...)")
			}
		})
	})

	if len(violations) > 0 {
		t.Fatalf("CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01: fmt.Errorf calls found in cellgen package files.\n"+
			"All errors must go through pkg/errcode (errcode.New / errcode.Wrap).\n"+
			"Violations (%d):\n  %s", len(violations), strings.Join(violations, "\n  "))
	}
}
