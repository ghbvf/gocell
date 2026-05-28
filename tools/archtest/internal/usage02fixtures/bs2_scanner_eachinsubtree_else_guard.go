package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Subtree-axis variant of bs2_scanner_else_guard.go: BS2 (else-branch return
// guard) on EachInSubtree. Main detector inspects only `ifStmt.Body` for the
// guard return; the else-branch shape evades it. Blind-spot reverse detector
// catches it.
// Expected: 0 main-detector hits, 1 blind-spot-detector hit.
func _(file *ast.File) bool {
	done := false
	scanner.EachInSubtree[ast.CallExpr](file, func(*ast.CallExpr) {
		if !done {
			done = true
		} else {
			return
		}
	})
	return done
}
