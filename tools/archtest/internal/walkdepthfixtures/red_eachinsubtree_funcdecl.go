package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// RED: the canonical violation — EachInSubtree (recursive subtree axis)
// instantiated with ast.FuncDecl. Expected: 1 hit at the EachInSubtree ident
// line. Declared as func _ (blank) so it is not flagged unused.
func _(file *ast.File) {
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		_ = fn
	})
}
