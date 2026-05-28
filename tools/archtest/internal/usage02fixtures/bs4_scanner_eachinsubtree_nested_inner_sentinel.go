package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// BS4 nested-walker form (subtree axis): outer EachInSubtree's callback
// contains an inner monitored walker call whose own callback carries the
// sentinel. The main detector's top-level CallExpr walk visits every
// monitored CallExpr in the file, including the lexically nested one — so
// both the outer call and the inner call are examined.
//
// After F3 fix (PR #1252 round-1 Review A), the detector's cb.Body scans
// use stopAtNestedFuncLit boundary: inner FuncLit's scope is skipped when
// scanning outer's callback body. So outer.assignedTrue does NOT include
// the inner sentinel, and outer is correctly NOT flagged (its own body has
// no sentinel — the iteration scope of `func(fd *ast.FuncDecl) { ... }`
// has neither `found = true` nor `if found { return }` directly).
//
// Inner call is examined independently by the top-level CallExpr walk; its
// own callback body DOES contain the sentinel, so it IS flagged.
//
// Expected: 1 main-detector hit (inner only). This is the correct semantics:
// each FuncLit is its own iteration scope and the sentinel belongs to the
// scope that declares + uses it.
func _(file *ast.File) bool {
	var out []ast.Node
	scanner.EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		_ = fd
		// Inner monitored walker with sentinel — inner is itself a violation.
		found := false
		scanner.EachInSubtree[ast.CallExpr](fd, func(call *ast.CallExpr) {
			if found {
				return
			}
			if call.Fun != nil {
				found = true
			}
		})
		if found {
			out = append(out, fd)
		}
	})
	return len(out) > 0
}
