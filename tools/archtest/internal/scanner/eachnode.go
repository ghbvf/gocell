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
// By node-kind selection only; there is no "skip subtree" callback.
// To constrain scope, pass a narrower sub-root or call EachInSubtree
// again on a chosen descendant.
//
// # Nil root
//
// Returns silently (no-op).
//
// ref: go/ast.Preorder — Go 1.23 stdlib typed iteration
// No *testing.T parameter: EachInSubtree is pure (no I/O, no parse errors).
// Callers call t.Errorf inside fn. The pure form also allows use from
// non-test paths (e.g. SCANNER-FRAMEWORK-USAGE-01 self-test walkers).
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
// CommClause from SelectStmt.Body, top-level FuncDecl/GenDecl from a
// *ast.File (each concrete decl kind passed as N, since ast.Decl is an
// interface and would fail the `*S` constraint), top-level Stmt from a
// *ast.BlockStmt (e.g. fn.Body's direct statements).
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

// FindFirstChild walks root's direct children (depth = 1, identical
// semantics to [EachInChildren]) and returns the first child whose concrete
// type is N and for which predicate returns true, together with ok = true.
// If no direct child matches, it returns the zero value and ok = false.
//
// FindFirstChild is the typed funnel that replaces the hand-written
// closure+done/found sentinel idiom:
//
//	// before — Soft: hand-rolled sentinel, scoping/guard easy to get wrong
//	found := false
//	scanner.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
//		if found { return }
//		if match(kv) { found = true }
//	})
//
//	// after — early-return implicit, no caller-held flag
//	_, found := scanner.FindFirstChild[ast.KeyValueExpr](lit,
//		func(kv *ast.KeyValueExpr) bool { return match(kv) })
//
// The early-return is implicit (predicate is not invoked again after the
// first match), there is no caller-exposed done flag, and picking the wrong
// N (an interface such as ast.Expr instead of a concrete *S) is a compile
// error via the same `interface { *S; ast.Node }` constraint as
// EachInChildren. Enforced against regression by archtest
// SCANNER-FRAMEWORK-USAGE-02 (allowlist = 0).
//
// # Depth
//
// depth = 1 only. Grandchildren are never inspected and root itself is
// never passed to predicate (see [EachInChildren]).
//
// # Subtree variant
//
// Recursive (subtree) find-first is intentionally NOT provided here — that
// is an orthogonal coverage axis tracked as backlog FINDFIRSTINSUBTREE-API-01
// (separate-trigger upgrade, 037 plan §1.1 scope boundary), not this
// funnel's upstream hardening path.
//
// # Nil root
//
// Returns (zero, false) silently (no-op), matching EachInChildren.
//
// Implementation reuses EachInChildren wholesale; the single sentinel the
// codebase-wide ban removes from business code is internalized here exactly
// once, under central governance.
func FindFirstChild[S any, N interface {
	*S
	ast.Node
}](root ast.Node, predicate func(N) bool) (N, bool) {
	var match N
	var found bool
	EachInChildren[S, N](root, func(n N) {
		if found {
			return
		}
		if predicate(n) {
			match, found = n, true
		}
	})
	return match, found
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

// EachInSubtreeStopAt traverses root's subtree like [EachInSubtree], invoking
// fn for each *N encountered, but stops descending into any non-root node for
// which stopAt returns true. The boundary node itself is NOT visited as N
// even if its type matches (it is excluded along with its subtree).
//
// Use this for archtest rules whose presence-check must IGNORE syntactic
// scopes that do not execute at the rule's site of interest — e.g. a
// funnel-call-presence check on a test body that must not credit funnel
// calls inside a nested *ast.FuncLit dead closure (the closure body runs
// only if the closure is invoked, which a static AST scan cannot prove).
//
// Picking EachInSubtreeStopAt over EachInSubtree is the third member of the
// typed-function-choice-for-walk-depth Hard template (ai-collab.md §"Hard
// 范本" #1, alongside EachInChildren depth=1 and EachInSubtree full-recursive):
// boundary-aware recursion is its own depth semantic, so picking the wrong
// walker = picking the wrong API name and fails archtest at the call site.
//
// # Root handling
//
// root is ALWAYS entered regardless of stopAt — stopAt only applies to
// non-root nodes, so a stopAt predicate may simply check the node's type
// (`_, isFL := n.(*ast.FuncLit); return isFL`) without manually excluding
// root. If root itself is of type N it is passed to fn before descent begins.
//
// # Nil root
//
// Returns silently (no-op).
//
// # Nil stopAt
//
// A nil stopAt is treated as the zero predicate (always false), making
// EachInSubtreeStopAt[N](root, nil, fn) equivalent to EachInSubtree[N](root, fn).
// Callers should prefer the simpler EachInSubtree in that case for clarity.
//
// ref: go/ast.Walk — Go stdlib Visitor pattern (descent control via returning
// nil from Visit).
func EachInSubtreeStopAt[S any, N interface {
	*S
	ast.Node
}](root ast.Node, stopAt func(ast.Node) bool, fn func(N)) {
	if root == nil {
		return
	}
	v := &subtreeStopAtVisitor[N]{fn: fn, stopAt: stopAt, atRoot: true}
	ast.Walk(v, root)
}

// subtreeStopAtVisitor implements ast.Visitor for boundary-aware recursive
// traversal. The walker visits root + every descendant in preorder, except
// that any non-root node for which stopAt returns true is skipped entirely
// (no fn invocation for the boundary node, no descent into its children).
type subtreeStopAtVisitor[N ast.Node] struct {
	fn     func(N)
	stopAt func(ast.Node) bool
	atRoot bool
}

func (v *subtreeStopAtVisitor[N]) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		// ast.Walk signals "children done" by calling Visit(nil) after each
		// subtree. Treat as no-op to match EachInChildren convention.
		return nil
	}
	if v.atRoot {
		v.atRoot = false
		if typed, ok := n.(N); ok {
			v.fn(typed)
		}
		return v
	}
	if v.stopAt != nil && v.stopAt(n) {
		// Boundary: skip this node and its subtree. The boundary node is
		// NOT passed to fn even if it matches N — fail-closed against
		// "boundary kind also accidentally credited" semantics.
		return nil
	}
	if typed, ok := n.(N); ok {
		v.fn(typed)
	}
	return v
}
