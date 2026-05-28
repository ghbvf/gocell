package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Subtree-axis variant of bs1_scanner_nonliteral_rhs.go: BS1 (sentinel set
// via non-literal-true RHS) on EachInSubtree. Main detector misses it (key
// is `= true`); blind-spot reverse detector catches it.
// Expected: 0 main-detector hits, 1 blind-spot-detector hit.
func _(file *ast.File, hit bool) bool {
	done := false
	scanner.EachInSubtree[ast.CallExpr](file, func(*ast.CallExpr) {
		if done {
			return
		}
		done = hit
	})
	return done
}
