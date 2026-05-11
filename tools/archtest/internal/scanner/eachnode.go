package scanner

import "go/ast"

// EachInSubtree iterates every node of kind N in the sub-tree rooted at root
// (preorder, recursive), calling fn with the already-typed node. Backed by
// Go 1.23 stdlib [ast.Preorder] — single backend, no cache, no external
// dependency.
//
// N is constrained via `interface { *S; ast.Node }` so it must be a concrete
// pointer type to a node struct. Callers write
//
//	scanner.EachInSubtree[ast.CallExpr](fc.File, func(call *ast.CallExpr) { ... })
//
// where Go's type inference fills in N=*ast.CallExpr from S=ast.CallExpr.
// Wrong N is a compile error, not a runtime panic. For example,
// `scanner.EachInSubtree[ast.Expr](file, ...)` fails to compile because
// ast.Expr is an interface and does not satisfy `*S` (must be a concrete
// pointer).
//
// # Choosing between EachInSubtree and [EachInChildren]
//
// Walk depth is a compile-time choice: different API names express
// different traversal semantics, so picking the wrong depth is statically
// visible at the call site rather than hidden in runtime AST behavior.
//
//   - EachInSubtree: recursive over the full sub-tree (root + every
//     descendant). Use when the rule reasons over all positions in a
//     function body, file, or expression — e.g. "any FuncDecl in the file",
//     "any IfStmt nested anywhere in fn.Body".
//   - [EachInChildren]: depth-1 only (root's direct children). Use when the
//     rule reasons over a container's immediate elements — KeyValueExpr
//     elements of a CompositeLit, CaseClause elements of a SwitchStmt.Body,
//     CommClause elements of a SelectStmt.Body, top-level Decl of a File.
//
// The dual-semantic fixtures in eachnode_test.go (T1+T2 share a nested
// CompositeLit fixture; subtree finds nested results, children does not)
// anchor the difference with RED tests, so the depth contract is not
// just godoc convention.
//
// # Pruning
//
// By node-kind selection only. To skip a sub-tree, register sibling
// handlers on parent kinds and check ancestor in handler body, OR call
// EachInSubtree again with the chosen sub-root.
//
// # Why no testing.T parameter (unlike [EachFile])
//
// EachInSubtree is pure — no parse error, no I/O, no t.Fatalf path.
// Callers' own t.Errorf inside fn handles violations. The pure-function
// form is reusable from non-test paths (forbiddenWalkRefs in
// scanner_framework_usage_test.go is the production type-aware walker
// called from inside the SCANNER-FRAMEWORK-USAGE-01 self-test; threading
// a *testing.T through it would be vestigial).
//
// # Nil root
//
// Returns silently (no-op).
//
// ref: go/ast.Preorder — Go 1.23 stdlib typed iteration
func EachInSubtree[S any, N interface {
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

// EachInChildren iterates only the DIRECT children of root (depth = 1),
// calling fn for any child whose type matches N. The root itself is never
// passed to fn; grandchildren are never visited.
//
// Implementation uses stdlib [ast.Walk] with a depth-1 visitor: the first
// Visit receives root and returns the visitor (recurse into root's
// children); each subsequent Visit processes any typed child and returns
// nil (halt at depth 1).
//
// Use for "container's direct elements" semantics: KeyValueExpr from a
// CompositeLit, CaseClause from SwitchStmt.Body / TypeSwitchStmt.Body,
// CommClause from SelectStmt.Body, top-level Decl from a *ast.File,
// top-level Stmt from a *ast.BlockStmt (e.g. fn.Body's direct statements).
//
// See [EachInSubtree] for the recursive variant and for the depth-choice
// decision criteria.
//
// # Nil root
//
// Returns silently (no-op).
//
// ref: go/ast.Walk — Go stdlib Visitor pattern (depth control via returning
// nil from Visit).
func EachInChildren[S any, N interface {
	*S
	ast.Node
}](root ast.Node, fn func(N)) {
	if root == nil {
		return
	}
	v := &childrenVisitor[N]{fn: fn, atRoot: true}
	ast.Walk(v, root)
}

// childrenVisitor implements ast.Visitor for depth-1 traversal:
//
//   - The first Visit call receives root itself; flip atRoot to false and
//     return v so ast.Walk recurses into root's children.
//   - Each subsequent Visit either processes the typed child (when n matches
//     N) or ignores it, then returns nil — ast.Walk treats a nil return as
//     "do not recurse into n's children", halting traversal at depth 1.
//   - ast.Walk also calls Visit(nil) after finishing each node's children;
//     treat that as a no-op.
type childrenVisitor[N ast.Node] struct {
	fn     func(N)
	atRoot bool
}

func (v *childrenVisitor[N]) Visit(n ast.Node) ast.Visitor {
	if v.atRoot {
		v.atRoot = false
		return v
	}
	if n == nil {
		return nil
	}
	if typed, ok := n.(N); ok {
		v.fn(typed)
	}
	return nil
}
