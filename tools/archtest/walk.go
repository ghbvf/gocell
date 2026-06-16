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
// Do NOT instantiate this (or [EachInSubtreeStopAt]) with ast.FuncDecl: a
// *ast.FuncDecl is always a direct *ast.File child (Go forbids nested func
// declarations — nested funcs are *ast.FuncLit), so the recursive descent is
// pure waste and the wrong depth semantic. Use [EachInChildren][ast.FuncDecl].
// Enforced by WALK-DEPTH-API-EACHCHILDREN-01
// (walk_depth_funcdecl_children_test.go).
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

// EachInSubtreeStopAt traverses root's subtree like [EachInSubtree], invoking
// fn for each *N encountered, but stops descending into any non-root node for
// which stopAt returns true. The boundary node itself is NOT visited as N
// even if its type matches. Third depth-semantic member of the
// typed-function-choice Hard template, alongside [EachInSubtree] /
// [EachInChildren] (see ai-robust.md §"Hard 范本目录" #1).
//
// Wrapper around [scanner.EachInSubtreeStopAt].
func EachInSubtreeStopAt[S any, N interface {
	*S
	ast.Node
}](root ast.Node, stopAt func(ast.Node) bool, fn func(N)) {
	scanner.EachInSubtreeStopAt[S, N](root, stopAt, fn)
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

// FindFirstInSubtree walks root's entire subtree (preorder, recursive) and
// returns the first node of kind N satisfying predicate. ok=false when no
// node matches. Subtree-depth twin of [FindFirstChild]; the depth choice is
// a typed function-name selection per ai-robust.md AI-robust Hard 范本 #1
// "typed function choice for walk depth".
//
// Compared to the manual `EachInSubtree + closure sentinel` idiom,
// FindFirstInSubtree internalizes the early-return state: no caller-held
// flag, wrong N is a compile error (interface{*S; ast.Node}), find-first
// semantic encoded in the function name. Root IS included in the search
// (mirrors [scanner.EachInSubtree] preorder semantics), contrasting
// FindFirstChild which excludes root. This is the only allowed subtree-depth
// early-return shape in archtest rules — the closure-sentinel idiom over
// `EachInSubtree` is banned by SCANNER-FRAMEWORK-USAGE-02 (allowlist 0),
// alongside its depth-1 sibling over EachInChildren. The
// FINDFIRSTINSUBTREE-API-01 work-stream label refers to the subtree-axis
// migration that folded into this single rule; there is no separate rule ID.
//
// Wrapper around [scanner.FindFirstInSubtree] — the only call path to
// scanner for archtest authors. Pure delegation, no behavior change.
func FindFirstInSubtree[S any, N interface {
	*S
	ast.Node
}](root ast.Node, predicate func(N) bool) (N, bool) {
	return scanner.FindFirstInSubtree[S, N](root, predicate)
}

// FindFirstInSubtreeStopAt walks root's subtree like [FindFirstInSubtree] but
// stops descending into any non-root node for which stopAt returns true —
// the boundary-aware sibling of FindFirstInSubtree, completing the typed
// function choice matrix {EachIn*, FindFirst*} × {Children, Subtree,
// SubtreeStopAt}. Use this when the rule needs first-match semantics AND
// must respect a closure / scope boundary (e.g. don't credit a match inside
// a nested *ast.FuncLit because that lives in its own iteration scope).
//
// Picking FindFirstInSubtreeStopAt over the boundary-less FindFirstInSubtree
// is a typed function name selection per ai-robust.md AI-robust Hard 范本 #1
// "typed function choice for walk depth"; archtest authors must NOT fall
// back to the manual `EachInSubtreeStopAt + closure-sentinel` idiom — that
// shape is banned by SCANNER-FRAMEWORK-USAGE-02 (allowlist 0) on the same
// grounds as the boundary-less variant.
//
// Wrapper around [scanner.FindFirstInSubtreeStopAt] — the only call path to
// scanner for archtest authors. Pure delegation, no behavior change.
func FindFirstInSubtreeStopAt[S any, N interface {
	*S
	ast.Node
}](root ast.Node, stopAt func(ast.Node) bool, predicate func(N) bool) (N, bool) {
	return scanner.FindFirstInSubtreeStopAt[S, N](root, stopAt, predicate)
}
