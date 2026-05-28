package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Subtree-axis variant of bs3_scanner_assign_outside.go: BS3 (guard inside
// callback, `= true` outside callback in enclosing function body) on
// EachInSubtree. Main detector misses it because the in-callback
// `assignedTrue` map stays empty. Blind-spot reverse detector catches it as a
// non-functional sentinel scoping shape.
// Expected: 0 main-detector hits, 1 blind-spot-detector hit.
func _(file *ast.File) bool {
	done := false
	scanner.EachInSubtree[ast.CallExpr](file, func(*ast.CallExpr) {
		if done {
			return
		}
	})
	done = true
	return done
}
