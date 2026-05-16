package archtest

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// EachInSubtree iterates every node of kind N in the sub-tree rooted at root
// (preorder, recursive). N is constrained to a concrete pointer type via
// `interface { *S; ast.Node }`, so callers write
//
//	archtest.EachInSubtree[ast.CallExpr](pass.Files[0], func(call *ast.CallExpr) { ... })
//
// and Go's type inference fills N=*ast.CallExpr from S=ast.CallExpr. Wrong N
// (e.g. an interface like ast.Expr) is a compile-time error.
//
// See [EachInChildren] for depth-1 traversal. The depth choice is a compile-
// time API selection (different function names express different semantics)
// rather than a runtime parameter.
//
// Wrapper around [scanner.EachInSubtree] — the only call path to scanner
// for archtest authors. Pure delegation, no behavior change.
func EachInSubtree[S any, N interface {
	*S
	ast.Node
}](root ast.Node, fn func(N)) {
	scanner.EachInSubtree[S, N](root, fn)
}

// EachInChildren iterates only the DIRECT children of root (depth = 1).
// Use for "container's immediate elements" semantics (KeyValueExpr from a
// CompositeLit, CaseClause from SwitchStmt.Body, etc.). See [EachInSubtree]
// for the recursive variant.
//
// Wrapper around [scanner.EachInChildren].
func EachInChildren[S any, N interface {
	*S
	ast.Node
}](root ast.Node, fn func(N)) {
	scanner.EachInChildren[S, N](root, fn)
}

// StringLitValue returns the unquoted value of a STRING-kind [*ast.BasicLit].
// Returns ok=false for nil, non-STRING, or malformed quoted literals.
//
// Wrapper around [scanner.StringLitValue].
func StringLitValue(lit *ast.BasicLit) (string, bool) {
	return scanner.StringLitValue(lit)
}

// ReceiverTypeName extracts the base type name from a method-receiver type
// expression. Handles *T / T / T[P] / T[P,Q]; returns "" for any other form.
//
// Wrapper around [scanner.ReceiverTypeName].
func ReceiverTypeName(expr ast.Expr) string {
	return scanner.ReceiverTypeName(expr)
}

// FindFirstChild scans the DIRECT children of root and returns the first node
// of kind N satisfying predicate. ok=false when no child matches.
//
// Compared to the manual `EachInChildren + done sentinel` idiom, FindFirstChild
// internalizes the early-return state: there is no caller-held flag, the wrong
// N is a compile error (interface{*S; ast.Node}), and the find-first semantic
// is encoded in the function name itself. This is the only allowed depth-1
// early-return shape in archtest rules — enforced by SCANNER-FRAMEWORK-USAGE-02.
//
// Wrapper around [scanner.FindFirstChild] — the only call path to scanner for
// archtest authors. Pure delegation, no behavior change. After 040 Stage 4
// seals internal/scanner, this façade is the only reachable path.
func FindFirstChild[S any, N interface {
	*S
	ast.Node
}](root ast.Node, predicate func(N) bool) (N, bool) {
	return scanner.FindFirstChild[S, N](root, predicate)
}
