package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Subtree-axis canonical form: FindFirstInSubtree[N] with a
// `func(N) bool` predicate. The early-return is encoded in the API name and
// in the predicate's bool return value; no caller-held sentinel exists.
// Expected: 0 main-detector hits, 0 blind-spot hits.
func _(file *ast.File) bool {
	_, ok := scanner.FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		return call.Fun != nil
	})
	return ok
}
