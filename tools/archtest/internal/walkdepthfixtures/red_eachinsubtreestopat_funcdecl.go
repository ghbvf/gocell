package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// RED: the boundary-aware subtree-axis twin — EachInSubtreeStopAt with
// ast.FuncDecl. The stopAt boundary is moot at depth-1, so this is the same
// wrong typed choice; banned to close the trivial-bypass vector (swap
// EachInSubtree → EachInSubtreeStopAt to evade). Expected: 1 hit. Declared as
// func _ (blank) so it is not flagged unused.
func _(file *ast.File) {
	scanner.EachInSubtreeStopAt[ast.FuncDecl](
		file,
		func(ast.Node) bool { return false },
		func(fn *ast.FuncDecl) { _ = fn },
	)
}
