package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// RED via a LOCAL type alias: `type FD = ast.FuncDecl` then EachInSubtree[FD].
// The element type arg is an *ast.Ident (FD), NOT an *ast.SelectorExpr, so the
// pre-fix syntactic detector (which required a selector + info.Uses[sel.Sel])
// missed it — defeating the godoc's "an alias cannot disguise either side"
// promise. The type-identity detector (info.Types[typeArg].Type +
// types.Unalias) resolves FD's element type to go/ast.FuncDecl regardless of
// the syntactic form. Expected: 1 hit. Declared as func _ (blank) so it is not
// flagged unused.
func _(file *ast.File) {
	type FD = ast.FuncDecl
	scanner.EachInSubtree[FD](file, func(fn *ast.FuncDecl) {
		_ = fn
	})
}
