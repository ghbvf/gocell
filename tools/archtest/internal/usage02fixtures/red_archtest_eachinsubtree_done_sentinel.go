package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest"
)

// Archtest-façade variant of red_scanner_eachinsubtree_done_sentinel.go:
// same sentinel shape on the subtree axis, but invoked through the 040
// façade archtest.EachInSubtree (which delegates to scanner.EachInSubtree).
// monitoredEachWalkerCallee accepts both packages, so this fixture asserts
// the façade is covered end-to-end by the typed pipeline.
// Expected: 1 main-detector hit.
func _(file *ast.File) bool {
	done := false
	archtest.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if done {
			return
		}
		if call.Fun != nil {
			done = true
		}
	})
	return done
}
