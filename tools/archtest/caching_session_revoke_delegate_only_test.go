// INVARIANT: CACHING-SESSION-REVOKE-DELEGATE-ONLY-01
// INVARIANT: CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01
//
// Package archtest — two related rules on (*CachingSessionStore) in
// adapters/redis, governing where session-cache mutation may appear in the two
// revoke methods. The single security invariant both rules serve is: a cache
// mutation must NEVER run inside the database transaction that performs the
// revoke (an in-tx cache.Delete/Set races with concurrent re-population from the
// still-uncommitted PG row, extending the stale window to 2×TTL — the
// historical Q1-A failure mode rejected in PR #524's third-round review).
//
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 — (*CachingSessionStore).RevokeForSubject
//   must be EXACTLY one ReturnStmt delegating to s.inner.RevokeForSubject(args...).
//   No cache op, no extra statement. RevokeForSubject needs no cache eviction at
//   all: credentialinvalidate.Apply co-tx bumps users.authz_epoch, and
//   sessionvalidate fail-closes any stale cached view on the epoch mismatch
//   regardless of cache state. (Subject-wide cache purge for the future case
//   where user state IS cached is deferred — AUTH-CACHE-SUBJECT-REVERSE-INDEX-01
//   / gh #793.)
//
// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 — (*CachingSessionStore).Revoke (#796)
//   must (A) delegate to s.inner.Revoke(ctx, id), and (B) confine every
//   s.cache.Delete / s.cache.Set to the body of a persistence.RegisterAfterCommit
//   hook literal. A cache mutation anywhere in the Revoke body OUTSIDE such a hook
//   is a violation — that is the relocated 2×TTL protection: the rule does not
//   forbid the cache DEL, it forbids it from running in the tx body. The
//   sanctioned shape fires the DEL only after the revoke commit is durable.
//
// AI-robust grade: Hard (both rules; single-axis, not a funnel). The guards are
// archtest-bound (Go does not make the violated forms uncompilable), but form
// uniqueness is total:
//   - Delegate-only: "exactly one ReturnStmt whose callee is
//     s.inner.RevokeForSubject" has no gray zone.
//   - After-commit-DEL: "every s.cache.{Delete,Set} CallExpr is lexically within
//     a persistence.RegisterAfterCommit hook FuncLit body" is a position-
//     containment fact (token.Pos ∈ [Lbrace, Rbrace]) over a type-resolved
//     callee (RegisterAfterCommit via TypesInfo, alias-proof) — no gray zone.
//   The hook body's own purity (no tx / outbox writer) is enforced independently
//   and in parallel by AFTERCOMMIT-HOOK-PURE-TRANSIENT-01.
//
// Scanning tool: Run(t, Typed(...)) + ast.FuncDecl receiver-type check +
// ast.BlockStmt / ReturnStmt / CallExpr shape checks (delegate-only) +
// EachInSubtree[ast.CallExpr] + token.Pos containment + TypesInfo.ObjectOf
// callee resolution (after-commit-DEL). The cache field selector (<recv>.cache)
// is matched by AST field name, not type: the RED fixtures use a local fakeCache
// type so a *redis.Cache type assertion would miss them.
//
// Blind-spot self-check (ai-robust.md §"工具选定后强制盲区自检"):
//
//  1. RevokeForSubject multi-statement body: counted via len(body.List). Any
//     extra statement is caught regardless of type. Self-check:
//     TestCachingSessionRevoke_BlindSpot_RevokeForSubjectSingleStmt.
//  2. cache mutation via method-value (`fn := s.cache.Delete; fn(...)`): the
//     CallExpr's Fun is then an *ast.Ident, not the <recv>.cache.Delete
//     selector, so cacheMutationCall would not match. Self-check:
//     TestCachingSessionRevoke_BlindSpot_CacheMethodValue asserts no method-value
//     of cache.{Delete,Set} or inner.{Revoke,RevokeForSubject} in production.
//  3. reflect.Value.MethodByName invocation: AST-invisible. Self-check:
//     TestCachingSessionRevoke_BlindSpot_Reflect asserts no reflect MethodByName
//     of the revoke methods in production.
//  4. cache mutation reached through a same-package helper called from the
//     Revoke body (one level deep): not resolved here (the cache selector must
//     appear lexically in the Revoke body). This is the same documented Medium
//     residual as AFTERCOMMIT-HOOK-PURE-TRANSIENT-01/B4 and is structurally
//     defanged by RunAfterCommitHooks stripping the tx from the hook ctx.

package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cachingStoreReceiverType is the concrete receiver type name (without pointer).
const cachingStoreReceiverType = "CachingSessionStore"

// cachingStoreInnerField / cachingStoreCacheField are the decorator's fields.
const (
	cachingStoreInnerField = "inner"
	cachingStoreCacheField = "cache"
)

// delegateOnlyMethod is the method held to the strict single-delegate shape.
const delegateOnlyMethod = "RevokeForSubject"

// afterCommitMethod is the method held to the after-commit-DEL shape.
const afterCommitMethod = "Revoke"

// revokeMethodNames is the set of both revoke methods, used by the blind-spot
// scans that are method-agnostic.
var revokeMethodNames = map[string]bool{afterCommitMethod: true, delegateOnlyMethod: true}

// cacheMutationMethods are the cache-write method names confined to after-commit
// hooks.
var cacheMutationMethods = map[string]bool{"Delete": true, "Set": true}

// registerAfterCommitFuncName is the persistence funnel that schedules
// post-commit work; resolved by package path (not just name) for alias-safety.
const (
	registerAfterCommitFuncName = "RegisterAfterCommit"
	persistencePkgSuffix        = "/kernel/persistence"
)

// TestCachingSessionRevoke_01 enforces both CACHING-SESSION-REVOKE-DELEGATE-ONLY-01
// (RevokeForSubject) and CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 (Revoke) over
// adapters/redis production code, with RED/GREEN fixtures proving the detection
// mechanism on each deviation class.
func TestCachingSessionRevoke_01(t *testing.T) {
	t.Parallel()

	fixtureRoot := "./tools/archtest/testdata/caching_session_revoke_fixtures"
	fixtureCases := []struct {
		label string
		dir   string
		green bool // green fixture must have ZERO violations
	}{
		// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 (Revoke).
		{"Revoke_intx_delete", "revoke_intx_delete_red", false},
		{"Revoke_intx_set", "revoke_intx_set_red", false},
		{"Revoke_no_delegate", "revoke_nodelegate_red", false},
		{"Revoke_aftercommit", "revoke_aftercommit_green", true},
		// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 (RevokeForSubject).
		{"RFS_multi_stmt", "rfs_multistmt_red", false},
		{"RFS_cache_op", "rfs_cacheop_red", false},
		{"RFS_wrong_delegate", "rfs_wrongdelegate_red", false},
	}
	patterns := make([]string, 0, 1+len(fixtureCases))
	patterns = append(patterns, "./adapters/redis/...")
	for _, fix := range fixtureCases {
		patterns = append(patterns, fixtureRoot+"/"+fix.dir)
	}

	const prodPkgSuffix = "/adapters/redis"
	var prodViolations []string
	fixtureViolationCount := make(map[string]int, len(fixtureCases))

	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Fset == nil || p.Pkg == nil {
			return nil
		}
		pkgPath := p.Pkg.Path()
		if strings.HasSuffix(pkgPath, prodPkgSuffix) {
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				prodViolations = append(prodViolations, scanCachingRevokeViolations(p, file, rel)...)
			}
			return nil
		}
		for _, fix := range fixtureCases {
			if !strings.HasSuffix(pkgPath, "/"+fix.dir) {
				continue
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				fixtureViolationCount[fix.dir] += len(scanCachingRevokeViolations(p, file, rel))
			}
			break
		}
		return nil
	})

	sort.Strings(prodViolations)
	for _, v := range prodViolations {
		t.Log(v)
	}
	assert.Empty(t, prodViolations,
		"CACHING-SESSION-REVOKE-{DELEGATE-ONLY,AFTERCOMMIT-DEL}-01: RevokeForSubject must be a "+
			"single-statement delegate to s.inner.RevokeForSubject; Revoke must delegate to "+
			"s.inner.Revoke and confine every s.cache.{Delete,Set} to a persistence.RegisterAfterCommit "+
			"hook (no in-tx cache mutation — that is the 2×TTL race).")

	// RED/GREEN fixture self-check.
	for _, fix := range fixtureCases {
		if fix.green {
			require.Equalf(t, 0, fixtureViolationCount[fix.dir],
				"GREEN fixture self-check FAILED: %s — the sanctioned after-commit shape must have 0 "+
					"violations, got %d.", fix.label, fixtureViolationCount[fix.dir])
			continue
		}
		require.GreaterOrEqualf(t, fixtureViolationCount[fix.dir], 1,
			"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
				"Check that the fixture file has the correct deviation and is type-checkable.", fix.label)
	}
}

// scanCachingRevokeViolations dispatches each (*CachingSessionStore) revoke
// method to its rule: RevokeForSubject → delegate-only; Revoke → after-commit.
func scanCachingRevokeViolations(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		recvName, ok := cachingStoreReceiver(fn)
		if !ok {
			return
		}
		line := p.Fset.Position(fn.Pos()).Line
		switch fn.Name.Name {
		case delegateOnlyMethod:
			if v := checkRevokeDelegateBody(fn.Body, fn.Name.Name, recvName, paramNames(fn)); v != "" {
				out = append(out, fmt.Sprintf("%s:%d: CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: (*%s).%s: %s",
					rel, line, cachingStoreReceiverType, fn.Name.Name, v))
			}
		case afterCommitMethod:
			for _, v := range checkRevokeAfterCommitBody(p, fn, recvName) {
				out = append(out, fmt.Sprintf("%s:%d: CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01: (*%s).%s: %s",
					rel, line, cachingStoreReceiverType, fn.Name.Name, v))
			}
		}
	})
	return out
}

// cachingStoreReceiver returns the receiver var name if fn is a method on
// *CachingSessionStore, else ("", false).
func cachingStoreReceiver(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
		return "", false
	}
	starExpr, isStar := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !isStar {
		return "", false
	}
	ident, isIdent := starExpr.X.(*ast.Ident)
	if !isIdent || ident.Name != cachingStoreReceiverType {
		return "", false
	}
	if names := fn.Recv.List[0].Names; len(names) > 0 {
		return names[0].Name, true
	}
	return "", true // anonymous receiver — empty name still matches callee mismatch
}

// paramNames collects the ordered parameter ident names of fn.
func paramNames(fn *ast.FuncDecl) []string {
	var names []string
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, n := range field.Names {
				names = append(names, n.Name)
			}
		}
	}
	return names
}

// cacheHookRange is a lexical [lo, hi] span (a hook FuncLit body).
type cacheHookRange struct{ lo, hi token.Pos }

func posInCacheHookRange(pos token.Pos, ranges []cacheHookRange) bool {
	for _, r := range ranges {
		if pos >= r.lo && pos <= r.hi {
			return true
		}
	}
	return false
}

// checkRevokeAfterCommitBody enforces CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01:
//
//	(A) the body delegates to <recv>.inner.Revoke(...);
//	(B) every <recv>.cache.{Delete,Set} CallExpr is lexically inside a
//	    persistence.RegisterAfterCommit hook FuncLit body.
func checkRevokeAfterCommitBody(p *Pass, fn *ast.FuncDecl, recvName string) []string {
	var out []string
	var hookRanges []cacheHookRange
	var hasDelegate bool

	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if lit := registerAfterCommitHookLit(p, call); lit != nil && lit.Body != nil {
			hookRanges = append(hookRanges, cacheHookRange{lit.Body.Lbrace, lit.Body.Rbrace})
		}
		if isInnerDelegateCall(call, recvName, afterCommitMethod) {
			hasDelegate = true
		}
	})
	if !hasDelegate {
		out = append(out, "body must delegate to s."+cachingStoreInnerField+".Revoke(ctx, id)")
	}
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if m, ok := cacheMutationCall(call, recvName); ok && !posInCacheHookRange(call.Pos(), hookRanges) {
			out = append(out, fmt.Sprintf(
				"s.%s.%s called outside a persistence.RegisterAfterCommit hook — in-tx cache mutation "+
					"reintroduces the 2×TTL re-population race", cachingStoreCacheField, m))
		}
	})
	return out
}

// registerAfterCommitHookLit returns the hook func literal if call is a
// persistence.RegisterAfterCommit(ctx, func(...){...}) call (callee resolved by
// package path for alias-safety), else nil.
func registerAfterCommitHookLit(p *Pass, call *ast.CallExpr) *ast.FuncLit {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != registerAfterCommitFuncName {
		return nil
	}
	if p.TypesInfo != nil {
		fn, _ := p.TypesInfo.ObjectOf(sel.Sel).(*types.Func)
		if fn == nil || fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), persistencePkgSuffix) {
			return nil
		}
	}
	if len(call.Args) == 0 {
		return nil
	}
	lit, _ := call.Args[len(call.Args)-1].(*ast.FuncLit)
	return lit
}

// cacheMutationCall reports whether call is <recv>.cache.{Delete,Set}(...).
func cacheMutationCall(call *ast.CallExpr, recvName string) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !cacheMutationMethods[sel.Sel.Name] {
		return "", false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != cachingStoreCacheField {
		return "", false
	}
	if !recvIdentMatches(field.X, recvName) {
		return "", false
	}
	return sel.Sel.Name, true
}

// isInnerDelegateCall reports whether call is <recv>.inner.<method>(...).
func isInnerDelegateCall(call *ast.CallExpr, recvName, method string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != cachingStoreInnerField {
		return false
	}
	return recvIdentMatches(field.X, recvName)
}

// recvIdentMatches reports whether x is the receiver ident (an empty recvName,
// from an anonymous receiver, matches any ident).
func recvIdentMatches(x ast.Expr, recvName string) bool {
	id, ok := x.(*ast.Ident)
	if !ok {
		return false
	}
	return recvName == "" || id.Name == recvName
}

// checkRevokeDelegateBody validates that body is exactly:
//
//	{ return <recvName>.inner.<methodName>(paramNames...) }
//
// Returns a non-empty violation description string on failure, "" on pass.
func checkRevokeDelegateBody(body *ast.BlockStmt, methodName, recvName string, paramNames []string) string {
	if len(body.List) != 1 {
		return fmt.Sprintf("body has %d statement(s); want exactly 1", len(body.List))
	}
	retStmt, ok := body.List[0].(*ast.ReturnStmt)
	if !ok {
		return fmt.Sprintf("body's single statement is %T; want *ast.ReturnStmt", body.List[0])
	}
	if len(retStmt.Results) != 1 {
		return fmt.Sprintf("return has %d result(s); want exactly 1", len(retStmt.Results))
	}
	callExpr, ok := retStmt.Results[0].(*ast.CallExpr)
	if !ok {
		return fmt.Sprintf("return result is %T; want *ast.CallExpr", retStmt.Results[0])
	}
	outerSel, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return fmt.Sprintf("callee is %T; want selector expr <recv>.%s.%s", callExpr.Fun, cachingStoreInnerField, methodName)
	}
	if outerSel.Sel.Name != methodName {
		return fmt.Sprintf("callee delegates to .%s; want .%s (same-method-name invariant)", outerSel.Sel.Name, methodName)
	}
	innerSel, ok := outerSel.X.(*ast.SelectorExpr)
	if !ok {
		return fmt.Sprintf("callee X is %T; want <recv>.%s selector", outerSel.X, cachingStoreInnerField)
	}
	if innerSel.Sel.Name != cachingStoreInnerField {
		return fmt.Sprintf("callee accesses field .%s; want .%s", innerSel.Sel.Name, cachingStoreInnerField)
	}
	recvIdent, ok := innerSel.X.(*ast.Ident)
	if !ok {
		return fmt.Sprintf("callee receiver is %T; want *ast.Ident (receiver variable)", innerSel.X)
	}
	if recvName != "" && recvIdent.Name != recvName {
		return fmt.Sprintf("callee receiver is %q; want method receiver %q", recvIdent.Name, recvName)
	}
	if len(callExpr.Args) != len(paramNames) {
		return fmt.Sprintf("callee has %d arg(s); want %d (one per param)", len(callExpr.Args), len(paramNames))
	}
	i := 0
	mismatch := ""
	EachInChildren[ast.Ident](callExpr, func(argIdent *ast.Ident) {
		if mismatch != "" || i >= len(paramNames) {
			return
		}
		if argIdent.Name != paramNames[i] {
			mismatch = fmt.Sprintf("callee arg[%d] is %q; want param ident %q", i, argIdent.Name, paramNames[i])
		}
		i++
	})
	if mismatch != "" {
		return mismatch
	}
	if i != len(paramNames) {
		return fmt.Sprintf("callee has %d plain-ident arg(s); want %d (some args are non-Ident expressions)", i, len(paramNames))
	}
	return ""
}

// ─── Blind-spot self-check tests ────────────────────────────────────────────

// TestCachingSessionRevoke_BlindSpot_RevokeForSubjectSingleStmt asserts the
// production RevokeForSubject body stays single-statement (Revoke is legitimately
// multi-statement after #796, so only the delegate-only method is checked here).
func TestCachingSessionRevoke_BlindSpot_RevokeForSubjectSingleStmt(t *testing.T) {
	t.Parallel()
	var multiStmtFound bool
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./adapters/redis/..."}), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			if strings.HasSuffix(p.Rel(file), "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if _, ok := cachingStoreReceiver(fn); !ok || fn.Name.Name != delegateOnlyMethod {
					return
				}
				if fn.Body != nil && len(fn.Body.List) > 1 {
					multiStmtFound = true
				}
			})
		}
		return nil
	})
	assert.False(t, multiStmtFound,
		"production RevokeForSubject body must be single-statement (delegate-only); the rfs_multistmt_red fixture is the RED-state mirror")
}

// TestCachingSessionRevoke_BlindSpot_CacheMethodValue asserts that method-value
// assignment of cache.{Delete,Set} or inner.{Revoke,RevokeForSubject} does NOT
// appear in adapters/redis production code — a method value would let a cache
// mutation be invoked through an *ast.Ident, bypassing the selector-shape check.
func TestCachingSessionRevoke_BlindSpot_CacheMethodValue(t *testing.T) {
	t.Parallel()
	var violations []string
	watched := func(name string) bool { return cacheMutationMethods[name] || revokeMethodNames[name] }
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./adapters/redis/..."}), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if watched(sel.Sel.Name) {
						line := p.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: method-value assignment of %s detected — blind spot for the selector-shape check",
							rel, line, sel.Sel.Name))
					}
				})
			})
		}
		return nil
	})
	assert.Empty(t, violations,
		"CACHING-SESSION-REVOKE blind-spot: method-value assignment of a cache mutation / inner delegate "+
			"found in adapters/redis production code — refactor to a direct call so the archtest stays complete.")
}

// TestCachingSessionRevoke_BlindSpot_Reflect asserts that
// reflect.MethodByName("Revoke"/"RevokeForSubject") does NOT appear in
// adapters/redis production code.
func TestCachingSessionRevoke_BlindSpot_Reflect(t *testing.T) {
	t.Parallel()
	var violations []string
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./adapters/redis/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			for _, hit := range scanReflectStringArgCalls(p, file, reflectMethodByName,
				func(n string) bool { return revokeMethodNames[n] }) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: CACHING-SESSION-REVOKE: reflect.MethodByName(%q) detected — archtest cannot see reflect-based invocations",
					rel, hit.Line, hit.Name))
			}
		}
		return nil
	})
	assert.Empty(t, violations,
		"CACHING-SESSION-REVOKE blind-spot: reflect.MethodByName of Revoke/RevokeForSubject found in adapters/redis.")
}
