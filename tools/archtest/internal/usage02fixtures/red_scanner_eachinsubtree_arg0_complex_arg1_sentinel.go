package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Companion fixture to red_scanner_eachinsubtree_arg0_complex_arg1_bs5.go:
// same arg[0] shape (CallExpr with FuncLit grandchild), but arg[1] is an
// inline FuncLit containing a standard sentinel. The detector must:
//  1. Identify arg[1] as the callback (via explicit positional anchor,
//     NOT via "first FuncLit direct child" implicit lookup which would
//     give the same answer here only by coincidence of depth-1 traversal).
//  2. Detect the sentinel inside arg[1]'s body.
//
// This anchors that arg[0]-subtree complexity does not corrupt sentinel
// detection — together with the BS5 sibling fixture, the pair pins the
// arg[1] anchor across both detection outcomes (BS5 / sentinel).
//
// Expected: 1 main-detector hit (standard sentinel form).
func _(file *ast.File) bool {
	found := false
	scanner.EachInSubtree[ast.CallExpr](
		// arg[0]: result of calling an inline FuncLit (FuncLit grandchild
		// at depth=2 of outer CallExpr — outside FindFirstChild depth=1).
		func() ast.Node { return file }(),
		// arg[1]: inline FuncLit callback with the standard sentinel idiom.
		func(call *ast.CallExpr) {
			if found {
				return
			}
			if call.Fun != nil {
				found = true
			}
		},
	)
	return found
}
