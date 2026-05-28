package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Subtree-axis variant of red_scanner_done_sentinel.go: same sentinel shape,
// scanner.EachInSubtree (subtree-recursive) instead of EachInChildren.
// Expected: 1 main-detector hit (standard sentinel form on subtree axis).
func _(file *ast.File) bool {
	done := false
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if done {
			return
		}
		if call.Fun != nil {
			done = true
		}
	})
	return done
}
