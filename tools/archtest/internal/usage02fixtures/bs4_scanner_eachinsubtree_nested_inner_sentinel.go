package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// BS4 nested-walker form (subtree axis): outer EachInSubtree's callback
// contains an inner monitored walker call whose own callback carries the
// sentinel. The main detector's top-level CallExpr walk visits every
// monitored CallExpr in the file, including the lexically nested one — so
// both the outer call and the inner call are examined. Because both walks
// (assignedTrue/IfStmt) over outer.cb.Body are subtree-recursive, they pick
// up the inner sentinel pattern, and the OUTER call is flagged in addition
// to the inner one.
// Expected: 2 main-detector hits (outer + inner).
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
