package scanner

import "go/ast"

// EachNode iterates every node of kind N in the sub-tree rooted at root,
// calling fn with the already-typed node. Backed by Go 1.23 stdlib
// [ast.Preorder] — single backend, no cache, no external dependency.
//
// N is constrained via `interface { *S; ast.Node }` so it must be a concrete
// pointer type to a node struct. Callers write
//
//	scanner.EachNode[ast.CallExpr](fc.File, func(call *ast.CallExpr) { ... })
//
// where Go's type inference fills in N=*ast.CallExpr from S=ast.CallExpr.
// Wrong N is a compile error, not a runtime panic. For example,
// `scanner.EachNode[ast.Expr](file, ...)` fails to compile because ast.Expr
// is an interface and does not satisfy `*S` (must be a concrete pointer).
//
// Pruning: by node-kind selection only. To skip a sub-tree, register sibling
// handlers on parent kinds and check ancestor in handler body, OR call
// EachNode again with the chosen sub-root.
//
// Why no testing.T parameter (unlike [EachFile]): EachNode is pure — no
// parse error, no I/O, no t.Fatalf path. Callers' own t.Errorf inside fn
// handles violations. The pure-function form is reusable from non-test paths
// (forbiddenWalkRefs in scanner_framework_usage_test.go is the production
// type-aware walker called from inside the SCANNER-FRAMEWORK-USAGE-01 self-
// test; threading a *testing.T through it would be vestigial).
//
// Nil root: returns silently (no-op).
//
// ref: go/ast.Preorder — Go 1.23 stdlib typed iteration
func EachNode[S any, N interface {
	*S
	ast.Node
}](root ast.Node, fn func(N)) {
	if root == nil {
		return
	}
	for n := range ast.Preorder(root) {
		if typed, ok := n.(N); ok {
			fn(typed)
		}
	}
}
