package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// GREEN anti-overreach case: EachInSubtree with a node type that genuinely
// nests at arbitrary depth (ast.CallExpr). The rule bans the recursive axis
// ONLY for the depth-1-only ast.FuncDecl, so this legitimate subtree walk MUST
// NOT be flagged. Expected: 0 violations. Declared as func _ (blank) so it is
// not flagged unused.
func _(file *ast.File) {
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		_ = call
	})
}
