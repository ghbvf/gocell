package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// GREEN baseline: the correct depth-1 helper for ast.FuncDecl. Expected: 0
// violations. Declared as func _ (blank) so it is not flagged unused.
func _(file *ast.File) {
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		_ = fn
	})
}
