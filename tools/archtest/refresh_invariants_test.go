package archtest

// refresh_invariants_test.go consolidates refresh-theme invariants:
//   - INVARIANT: REFRESH-CROSS-STORE-TX-01
//   - INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//   - INVARIANT: REFRESH-AMBIENT-TX-01

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleRefreshCrossStoreTX01 = "REFRESH-CROSS-STORE-TX-01"

// canonicalInvalidIndexFile is the only file allowed to define DetectInvalidIndexes.
const canonicalInvalidIndexFile = "adapters/postgres/schema_guard.go"

const ruleRefreshInvalidIndexSingleSource01 = "REFRESH-INVALID-INDEX-SINGLE-SOURCE-01"

const ruleRefreshAmbientTX01 = "REFRESH-AMBIENT-TX-01"

// INVARIANT: REFRESH-CROSS-STORE-TX-01
//
// refresh_cross_store_tx_test.go enforces REFRESH-CROSS-STORE-TX-01:
// cells/accesscore/slices/sessionrefresh/service.go Refresh method must wrap
// the validate→update→rotate sequence in a single s.txRunner.RunInTx call so
// that PG refresh-store and (eventually) PG session-repo writes share one
// commit boundary.
//
// The rule scans the AST:
//  1. Refresh body must contain exactly one s.txRunner.RunInTx(...) call.
//  2. The RunInTx call's second argument resolves to a *ast.FuncLit (either
//     inline `func(...) error { ... }` or an identifier bound to one).
//  3. The closure body must invoke at least one method on `s` — guards
//     against an empty/no-op wrap that satisfies (1) without doing work.
//  4. The forbidden bare-store calls (s.refreshStore.Peek / .Rotate,
//     s.sessionRepo.Update / .GetByID, s.userRepo.GetByID) must NOT appear
//     in Refresh's body outside the closure. They may live inside the
//     closure or in any helper method on s reachable through it (e.g.
//     s.refreshInTx) — any direct call from Refresh's top level escapes
//     the wrap.
//
// Cascade-revoke paths (s.refreshStore.RevokeSessionDetached) are allowed
// outside the closure: PR#395 detached-context invariant requires them to
// run on a context derived via ctxutil.WithDetachedTimeout.
func TestRefreshCrossStoreTX01(t *testing.T) {
	root := findModuleRoot(t)
	rel := "cells/accesscore/slices/sessionrefresh/service.go"
	abs := filepath.Join(root, filepath.FromSlash(rel))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoError(t, err, "%s: parse failed", rel)

	var refreshFunc *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if refreshFunc != nil || fn.Recv == nil || fn.Name.Name != "Refresh" {
			return
		}
		refreshFunc = fn
	})
	require.NotNil(t, refreshFunc, "%s: Refresh method not found in %s", ruleRefreshCrossStoreTX01, rel)

	// Find s.txRunner.RunInTx call(s) at the top level of Refresh body. The
	// second argument may be either an inline *ast.FuncLit or an identifier
	// referring to a closure assigned earlier in Refresh's body (the sessionlogin
	// `do := func(txCtx) error { ... }` pattern). Both are accepted.
	var runInTxCalls []*ast.CallExpr
	var closureArg ast.Expr
	scanner.EachInSubtree[ast.CallExpr](refreshFunc.Body, func(call *ast.CallExpr) {
		if !isTxRunnerRunInTxCall(call) {
			return
		}
		runInTxCalls = append(runInTxCalls, call)
		if len(call.Args) >= 2 {
			closureArg = call.Args[1]
		}
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
	scanner.EachInSubtree[ast.CallExpr](runInTxClosure.Body, func(call *ast.CallExpr) {
		if hasReceiverCall {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "s" {
			hasReceiverCall = true
		}
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

	// closureRange captures the [Lbrace, Rbrace] token range of the RunInTx
	// closure body so we can skip calls that are inside the desired location.
	closureLbrace := runInTxClosure.Body.Lbrace
	closureRbrace := runInTxClosure.Body.Rbrace
	scanner.EachInSubtree[ast.CallExpr](refreshFunc.Body, func(call *ast.CallExpr) {
		// Skip nodes inside the RunInTx closure body — those are the desired location.
		if call.Pos() > closureLbrace && call.Pos() < closureRbrace {
			return
		}
		name, ok := bareServiceFieldCall(call)
		if !ok {
			return
		}
		if _, isGuarded := guardedCalls[name]; !isGuarded {
			return
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, violation{line: pos.Line, name: name})
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
	scanner.EachInSubtree[ast.AssignStmt](body, func(assign *ast.AssignStmt) {
		// Build an index map from Ident pointer to position in Lhs.
		lhsIndex := make(map[*ast.Ident]int, len(assign.Lhs))
		scanner.EachInSubtree[ast.Ident](assign, func(id *ast.Ident) {
			for i, lhs := range assign.Lhs {
				if lhs == id {
					lhsIndex[id] = i
					break
				}
			}
		})
		for id, i := range lhsIndex {
			if id.Name != ident.Name {
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

// INVARIANT: REFRESH-INVALID-INDEX-SINGLE-SOURCE-01
//
// refresh_invalid_index_single_source_test.go enforces REFRESH-INVALID-INDEX-SINGLE-SOURCE-01:
// the function "DetectInvalidIndexes" must be declared (defined) in exactly one
// production (non-_test.go) Go file across the entire repository:
// adapters/postgres/schema_guard.go.
//
// Callers of DetectInvalidIndexes (e.g. migrator.go, cmd/corebundle/bundle_configcore_storage.go)
// are allowed. Only a second *declaration* (func DetectInvalidIndexes ...) would
// violate the rule, which would indicate B8 or future work introducing a
// parallel invalid-index check path outside schema_guard.
func TestRefreshInvalidIndexSingleSource01(t *testing.T) {
	root := findModuleRoot(t)

	type declarationSite struct {
		rel  string
		line int
	}
	var declarations []declarationSite

	scope := scanner.ModuleScope(root)
	scanner.EachFile(t, scope, parser.SkipObjectResolution|parser.ParseComments, func(t *testing.T, fc scanner.FileContext) {
		scanner.EachInSubtree[ast.FuncDecl](fc.File, func(fd *ast.FuncDecl) {
			if fd.Name.Name != "DetectInvalidIndexes" {
				return
			}
			// Only top-level function declarations (no receiver).
			if fd.Recv != nil {
				return
			}
			pos := fc.Fset.Position(fd.Pos())
			declarations = append(declarations, declarationSite{
				rel:  filepath.ToSlash(fc.Rel),
				line: pos.Line,
			})
		})
	})

	if len(declarations) == 0 {
		t.Fatalf("%s: DetectInvalidIndexes not declared anywhere — expected it in %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile)
	}

	if len(declarations) > 1 {
		t.Logf("%s: DetectInvalidIndexes declared in %d files (expected 1):", ruleRefreshInvalidIndexSingleSource01, len(declarations))
		for _, d := range declarations {
			t.Logf("  %s:%d", d.rel, d.line)
		}
	}

	assert.Len(t, declarations, 1,
		"%s: DetectInvalidIndexes must be declared in exactly one production file (%s); "+
			"found declarations in %d files — callers are allowed, new parallel definitions are not",
		ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, len(declarations))

	if len(declarations) == 1 {
		assert.Equal(t, canonicalInvalidIndexFile, declarations[0].rel,
			"%s: DetectInvalidIndexes must be declared in %s, not %s",
			ruleRefreshInvalidIndexSingleSource01, canonicalInvalidIndexFile, declarations[0].rel)
	}
}

// INVARIANT: REFRESH-AMBIENT-TX-01
//
// refresh_store_ambient_tx_test.go enforces REFRESH-AMBIENT-TX-01:
// adapters/postgres/refresh_store.go must not contain any direct pool.Begin /
// (*pgxpool.Pool).Begin / tx.Begin calls. After B2-A-08, Peek and Rotate
// delegate transaction management to the injected TxRunner; the store itself
// must not acquire transactions directly.
//
// The rule scans the AST for SelectorExpr calls whose Sel.Name is "Begin"
// where the receiver is a known pool-like identifier. It also catches bare
// method calls named "Begin" on any expression, since the only legitimate
// Begin callers in refresh_store.go would be pool or tx variables.
func TestRefreshAmbientTX01(t *testing.T) {
	root := findModuleRoot(t)
	rel := "adapters/postgres/refresh_store.go"
	abs := filepath.Join(root, filepath.FromSlash(rel))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoError(t, err, "%s: parse failed", rel)

	type violation struct {
		line int
		expr string
	}
	var violations []violation

	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Begin" {
			return
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, violation{
			line: pos.Line,
			expr: fmt.Sprintf("call to .Begin() at line %d", pos.Line),
		})
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s) in %s:", ruleRefreshAmbientTX01, len(violations), rel)
		for _, v := range violations {
			t.Logf("  line %d: .Begin() call — refresh_store must delegate to TxRunner, not acquire transactions directly", v.line)
		}
	}
	assert.Empty(t, violations,
		"%s: %s must not contain .Begin() calls; use injected TxRunner.RunInTx instead (B2-A-08)",
		ruleRefreshAmbientTX01, rel)
}
