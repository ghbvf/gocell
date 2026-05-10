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
// Trap (transitional, removed once PR-Φ-Hard ships): EachNode walks the
// entire sub-tree rooted at root (preorder). When the caller really wants
// only the direct children of a node — e.g. KeyValueExpr elements of a
// CompositeLit, CommClause elements of a SelectStmt, or the top-level
// Body.List of an *ast.IfStmt — DO NOT use EachNode; iterate the parent's
// child slice directly. Otherwise nested literals' inner nodes leak into
// outer-scope decisions (PR445-FU finding F1 was this exact pattern).
// PR-Φ-Hard splits this API into EachInSubtree[N] / EachInChildren[N] so
// the walk depth becomes a compile-time choice. Tracking: backlog item
// PR-Φ-HARD-EACHNODE-WALKDEPTH-01.
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
