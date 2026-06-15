package walkdepthfixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest"
)

// RED via the archtest façade (unqualified-style import of the public wrapper,
// pkg path github.com/ghbvf/gocell/tools/archtest) rather than scanner — this
// is the MORE common real-world form (archtest rule authors call the façade).
// Exercises the walkDepthArchtestPkgPath detection branch (the scanner-path
// fixtures only cover walkDepthScannerPkgPath). Expected: 1 hit. Declared as
// func _ (blank) so it is not flagged unused.
func _(file *ast.File) {
	archtest.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		_ = fn
	})
}
