package archtest

// refresh_cross_store_tx_test.go enforces REFRESH-CROSS-STORE-TX-01:
// cells/accesscore/slices/sessionrefresh/service.go Refresh method must wrap
// the validate→update→rotate sequence in a single s.txRunner.RunInTx call so
// that PG refresh-store and (eventually) PG session-repo writes share one
// commit boundary.
//
// The rule scans the AST:
//   1. Refresh body must contain exactly one s.txRunner.RunInTx(...) call.
//   2. The RunInTx call's second argument resolves to a *ast.FuncLit (either
//      inline `func(...) error { ... }` or an identifier bound to one).
//   3. The closure body must invoke at least one method on `s` — guards
//      against an empty/no-op wrap that satisfies (1) without doing work.
//   4. The forbidden bare-store calls (s.refreshStore.Peek / .Rotate,
//      s.sessionRepo.Update / .GetByID, s.userRepo.GetByID) must NOT appear
//      in Refresh's body outside the closure. They may live inside the
//      closure or in any helper method on s reachable through it (e.g.
//      s.refreshInTx) — any direct call from Refresh's top level escapes
//      the wrap.
//
// Cascade-revoke paths (s.refreshStore.RevokeSessionDetached) are allowed
// outside the closure: PR#395 detached-context invariant requires them to
// run on a context derived via ctxutil.WithDetachedTimeout.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleRefreshCrossStoreTX01 = "REFRESH-CROSS-STORE-TX-01"

// TestRefreshCrossStoreTX01 asserts that sessionrefresh.Service.Refresh
// wraps its body in exactly one s.txRunner.RunInTx call, and that the
// store-touching calls listed below appear inside that closure.
func TestRefreshCrossStoreTX01(t *testing.T) {
	root := findModuleRoot(t)
	rel := "cells/accesscore/slices/sessionrefresh/service.go"
	abs := filepath.Join(root, filepath.FromSlash(rel))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoError(t, err, "%s: parse failed", rel)

	var refreshFunc *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "Refresh" {
			continue
		}
		refreshFunc = fn
		break
	}
	require.NotNil(t, refreshFunc, "%s: Refresh method not found in %s", ruleRefreshCrossStoreTX01, rel)

	// Find s.txRunner.RunInTx call(s) at the top level of Refresh body. The
	// second argument may be either an inline *ast.FuncLit or an identifier
	// referring to a closure assigned earlier in Refresh's body (the sessionlogin
	// `do := func(txCtx) error { ... }` pattern). Both are accepted.
	var runInTxCalls []*ast.CallExpr
	var closureArg ast.Expr
	ast.Inspect(refreshFunc.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isTxRunnerRunInTxCall(call) {
			return true
		}
		runInTxCalls = append(runInTxCalls, call)
		if len(call.Args) >= 2 {
			closureArg = call.Args[1]
		}
		return true
	})

	require.Lenf(t, runInTxCalls, 1,
		"%s: %s Refresh must contain exactly 1 s.txRunner.RunInTx call (found %d) — wrap validate→update→rotate in a single outer transaction",
		ruleRefreshCrossStoreTX01, rel, len(runInTxCalls))
	require.NotNilf(t, closureArg, "%s: %s Refresh's RunInTx call must have a second argument",
		ruleRefreshCrossStoreTX01, rel)

	runInTxClosure := resolveClosureArg(refreshFunc.Body, closureArg)
	require.NotNilf(t, runInTxClosure,
		"%s: %s Refresh's RunInTx must receive a closure literal — inline func() or a local variable bound to one",
		ruleRefreshCrossStoreTX01, rel)

	// Guard against an empty/no-op closure that satisfies "exactly one RunInTx"
	// without actually doing work. The closure body must contain at least one
	// method call on `s`.
	var hasReceiverCall bool
	ast.Inspect(runInTxClosure.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "s" {
			hasReceiverCall = true
			return false
		}
		return true
	})
	require.Truef(t, hasReceiverCall,
		"%s: %s Refresh's RunInTx closure must invoke at least one method on `s` — an empty closure satisfies the wrap shape but does no work",
		ruleRefreshCrossStoreTX01, rel)

	// Forbidden bare-receiver call sites that must be inside the closure.
	// RevokeSession is in scope because it is the ambient-tx variant
	// (joins the caller's TX) — calling it from Refresh's top level would
	// escape the wrap. The detached variant RevokeSessionDetached is
	// intentionally NOT guarded: PR#395 cascade paths are required to
	// commit independently of the outer transaction.
	guardedCalls := map[string]bool{
		"refreshStore.Peek":          false,
		"refreshStore.Rotate":        false,
		"refreshStore.RevokeSession": false,
		"sessionRepo.Update":         false,
		"sessionRepo.GetByID":        false,
		"userRepo.GetByID":           false,
	}

	type violation struct {
		line int
		name string
	}
	var violations []violation

	ast.Inspect(refreshFunc.Body, func(n ast.Node) bool {
		// Skip nodes inside the RunInTx closure body — those are the desired location.
		if n == runInTxClosure {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := bareServiceFieldCall(call)
		if !ok {
			return true
		}
		if _, isGuarded := guardedCalls[name]; !isGuarded {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, violation{line: pos.Line, name: name})
		return true
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s) in %s:", ruleRefreshCrossStoreTX01, len(violations), rel)
		for _, v := range violations {
			t.Logf("  line %d: s.%s(...) called outside the RunInTx closure — move it inside the closure",
				v.line, v.name)
		}
	}
	assert.Empty(t, violations,
		"%s: %s Refresh must call s.refreshStore.Peek/Rotate, s.sessionRepo.Update/GetByID, and s.userRepo.GetByID only inside the closure",
		ruleRefreshCrossStoreTX01, rel)
}

// isTxRunnerRunInTxCall reports whether call is `s.txRunner.RunInTx(...)`.
func isTxRunnerRunInTxCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RunInTx" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "txRunner" {
		return false
	}
	ident, ok := inner.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "s"
}

// resolveClosureArg returns the *ast.FuncLit that arg refers to: either arg
// itself (inline closure) or the FuncLit bound to the identifier in body.
// When the identifier has multiple FuncLit assignments (`do := func(){...};
// do = func(){...}`), the LAST assignment wins — that is the value of `do`
// at the point of the RunInTx call site, matching Go's evaluation semantics.
// Reasoning about only the first assignment would let an attacker satisfy
// the structural guard with a non-trivial first FuncLit and reassign a
// no-op FuncLit before passing `do` to RunInTx.
// Returns nil if neither pattern matches.
func resolveClosureArg(body *ast.BlockStmt, arg ast.Expr) *ast.FuncLit {
	if fl, ok := arg.(*ast.FuncLit); ok {
		return fl
	}
	ident, ok := arg.(*ast.Ident)
	if !ok {
		return nil
	}
	var lastAssigned *ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			lid, ok := lhs.(*ast.Ident)
			if !ok || lid.Name != ident.Name {
				continue
			}
			if i >= len(assign.Rhs) {
				continue
			}
			fl, ok := assign.Rhs[i].(*ast.FuncLit)
			if !ok {
				// Non-FuncLit assignment to the identifier (e.g. another
				// variable, a function call, or nil) — it overrides any
				// prior FuncLit. Reset so we don't claim the previous one.
				lastAssigned = nil
				continue
			}
			lastAssigned = fl
		}
		return true
	})
	return lastAssigned
}

// bareServiceFieldCall reports whether call has the shape `s.<field>.<method>(...)`,
// returning "<field>.<method>" if so.
func bareServiceFieldCall(call *ast.CallExpr) (string, bool) {
	outer, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	inner, ok := outer.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := inner.X.(*ast.Ident)
	if !ok || ident.Name != "s" {
		return "", false
	}
	return inner.Sel.Name + "." + outer.Sel.Name, true
}
