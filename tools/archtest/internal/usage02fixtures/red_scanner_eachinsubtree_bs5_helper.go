package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// BS5 helper-func-value form (subtree axis): EachInSubtree second argument is
// a named function value, not an inline FuncLit. The callback's sentinel — if
// any — lives in a separately-declared body that the depth-1
// FindFirstChild[FuncLit] callback extraction cannot reach. Main detector
// reports this as a BS5 violation (allowlist 0; preventive ban).
// Expected: 1 main-detector hit (BS5 message).
func bs5HelperCallback(*ast.CallExpr) {
	// body intentionally non-empty; even if sentinel-free, the form is banned.
	_ = 0
}

func _(file *ast.File) {
	scanner.EachInSubtree[ast.CallExpr](file, bs5HelperCallback)
}
