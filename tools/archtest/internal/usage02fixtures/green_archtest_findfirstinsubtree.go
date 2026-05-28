package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest"
)

// Archtest-façade GREEN: the canonical subtree-axis find-first form invoked
// through archtest.FindFirstInSubtree (the 040 façade over
// scanner.FindFirstInSubtree). Pairs with green_scanner_findfirstinsubtree.go
// to verify GREEN coverage on both packages.
// Expected: 0 main-detector hits, 0 blind-spot-detector hits.
func _(file *ast.File) bool {
	_, ok := archtest.FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		return call.Fun != nil
	})
	return ok
}
