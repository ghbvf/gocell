// INVARIANT: CACHING-SESSION-REVOKE-DELEGATE-ONLY-01
//
// Package archtest — single-rule file for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
//
// Rule: (*CachingSessionStore).Revoke and (*CachingSessionStore).RevokeForSubject
// in adapters/redis must each have a body that is EXACTLY ONE statement: a
// ReturnStmt whose single result is a CallExpr of the form
// s.inner.<SAME-METHOD-NAME>(args...). Any deviation — >1 statement, a cache
// field access in the body, or a delegate to a different method name — fails
// the archtest.
//
// AI-rebust grade: Hard. The guard is archtest-bound (Go does not make the
// violated form uncompilable), but form-uniqueness is total: "exactly one
// ReturnStmt whose callee is s.inner.SameMethodName" has no gray zone.
// Any other shape fails CI.
//
// Scanning tool: RunTyped + ast.FuncDecl receiver-type check (syntactic) +
// ast.BlockStmt length check + ast.ReturnStmt / ast.CallExpr shape check.
// ResolveMethodCall is intentionally NOT used for the delegate callee check
// because we need to verify the method name matches the enclosing FuncDecl,
// not merely that the callee resolves to session.Store — the name symmetry
// invariant is stronger than interface resolution.
//
// Blind-spot self-check (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Multi-statement body: archtest counts len(body.List) — ANY extra
//     statement is caught regardless of its type. Self-check:
//     TestCachingSessionRevokeDelegateOnly_BlindSpot_MultiStmt asserts
//     absence of multi-statement Revoke/RevokeForSubject bodies in production.
//
//  2. Delegate via method-value store:
//     `fn := s.inner.Revoke; return fn(ctx, id)` — the return result is
//     *ast.CallExpr with Fun=*ast.Ident, not *ast.SelectorExpr, so the
//     inner-delegate check would pass for wrong reasons (body still has
//     2 stmts: assign + return). Caught by the >1 statement check.
//     Self-check: TestCachingSessionRevokeDelegateOnly_BlindSpot_MethodValue
//     asserts absence in production code.
//
//  3. reflect.Value.MethodByName("Revoke").Call(…): fully AST-invisible.
//     Self-check: TestCachingSessionRevokeDelegateOnly_BlindSpot_Reflect
//     asserts absence in production code.

package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revokeTargetMethods lists the method names on *CachingSessionStore that must
// conform to the pure-delegate body shape.
var revokeTargetMethods = map[string]bool{
	"Revoke":           true,
	"RevokeForSubject": true,
}

// cachingStoreReceiverType is the concrete receiver type name (without pointer).
const cachingStoreReceiverType = "CachingSessionStore"

// cachingStoreInnerField is the field name for the inner session.Store.
const cachingStoreInnerField = "inner"

// TestCachingSessionRevokeDelegateOnly_01 enforces
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: (*CachingSessionStore).Revoke and
// (*CachingSessionStore).RevokeForSubject in adapters/redis must each be a
// single-statement pure delegate to s.inner.<SameMethodName>(args...).
//
// Production code at adapters/redis/session_cache_store.go satisfies this
// invariant post-GREEN; the seven RED fixtures below mirror the rejected
// forms so the scanner's detection mechanism is itself verified.
//
// RED fixture verification — each fixture package embodies one violation
// class; all seven MUST be detected by scanRevokeDelegateViolations:
//
//   - F1 (testdata/.../f1_multi_stmt_red): Revoke body contains a second
//     statement before the return (logs/side-effects). Detects multi-stmt
//     bodies.
//   - F2 (testdata/.../f2_cache_delete_red): Revoke body calls
//     s.cache.Delete before delegating. Detects the historical Q1-A failure
//     mode this PR's #533 third-round review removed (in-tx cache.Delete
//     race vs PG commit).
//   - F3 (testdata/.../f3_cache_set_red): Revoke body calls s.cache.Set.
//     Detects any cache-side write injected into the revoke path (would
//     equally race vs PG commit; symmetrical with F2).
//   - F4 (testdata/.../f4_wrong_delegate_red): Revoke body delegates to a
//     differently-named inner method (e.g. RevokeForSubject). Detects
//     same-method-name invariant breakage that would silently route revoke
//     semantics to the wrong sink.
//   - F5 (testdata/.../f5_wrong_receiver_red): Revoke body delegates via a
//     different variable (other.inner.Revoke) instead of the method receiver
//     (s.inner.Revoke). Detects receiver-ident invariant breakage.
//   - F6 (testdata/.../f6_wrong_args_red): Revoke body delegates with
//     literal arguments (context.Background(), "") instead of the method's
//     own parameter idents. Detects args-ident invariant breakage.
//   - F7 (testdata/.../f7_cache_in_arg_red): Revoke body delegates with a
//     CallExpr as an argument (idFromCache(s.cache, id)) instead of a plain
//     parameter ident. Detects computed/derived args invariant breakage.
func TestCachingSessionRevokeDelegateOnly_01(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./adapters/redis/..."}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations, scanRevokeDelegateViolations(p, file, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: (*CachingSessionStore).Revoke and "+
			"(*CachingSessionStore).RevokeForSubject must each be a single-statement body "+
			"of the form `return s.inner.<SameMethodName>(args...)`. "+
			"Any cache operation or extra statement in these methods is a violation. "+
			"This rule locks Q1=B (Revoke no cache.Delete) and Q2=α (RevokeForSubject no cache op) "+
			"from the third-round review plan.")

	// RED fixture verification: each of the four fixture packages must have ≥ 1 detected violation.
	fixtureRoot := "./tools/archtest/testdata/caching_session_revoke_fixtures"
	for _, fix := range []struct {
		name    string
		pattern string
	}{
		{"F1_multi_stmt", fixtureRoot + "/f1_multi_stmt_red"},
		{"F2_cache_delete", fixtureRoot + "/f2_cache_delete_red"},
		{"F3_cache_set", fixtureRoot + "/f3_cache_set_red"},
		{"F4_wrong_delegate", fixtureRoot + "/f4_wrong_delegate_red"},
		{"F5_wrong_receiver", fixtureRoot + "/f5_wrong_receiver_red"},
		{"F6_wrong_args", fixtureRoot + "/f6_wrong_args_red"},
		{"F7_cache_in_arg", fixtureRoot + "/f7_cache_in_arg_red"},
	} {
		verifyRevokeDelegateRedFixture(t, fix.name, fix.pattern)
	}
}

// scanRevokeDelegateViolations walks a file's AST and finds any FuncDecl for
// (*CachingSessionStore).Revoke or (*CachingSessionStore).RevokeForSubject
// whose body deviates from the single-statement pure-delegate shape:
//
//	return s.inner.<SameMethodName>(args...)
//
// Deviations:
//   - body has ≠ 1 statement
//   - the single statement is not a ReturnStmt
//   - the ReturnStmt result count ≠ 1
//   - the single result is not a CallExpr
//   - the CallExpr Fun is not `*ast.SelectorExpr` with X=*ast.SelectorExpr `<recv>.inner` and Sel.Name==methodName
//   - the receiver ident in the callee's X does not match the method's own receiver name
//   - the arguments to the call are not all plain parameter idents (in order)
func scanRevokeDelegateViolations(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			return
		}
		if !revokeTargetMethods[fn.Name.Name] {
			return
		}
		// Receiver must be pointer to CachingSessionStore.
		recvType := fn.Recv.List[0].Type
		starExpr, isStar := recvType.(*ast.StarExpr)
		if !isStar {
			return
		}
		ident, isIdent := starExpr.X.(*ast.Ident)
		if !isIdent || ident.Name != cachingStoreReceiverType {
			return
		}
		if fn.Body == nil {
			return
		}

		// Derive the receiver name. Anonymous receivers have no Names — treat
		// as empty string so the check reliably detects any callee mismatch.
		recvName := ""
		if recvNames := fn.Recv.List[0].Names; len(recvNames) > 0 {
			recvName = recvNames[0].Name
		}

		// Collect param ident names in order (one Field may name multiple same-type params).
		var paramNames []string
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				for _, n := range field.Names {
					paramNames = append(paramNames, n.Name)
				}
			}
		}

		methodName := fn.Name.Name
		line := p.Fset.Position(fn.Pos()).Line
		violation := checkRevokeDelegateBody(fn.Body, methodName, recvName, paramNames)

		if violation != "" {
			out = append(out, fmt.Sprintf(
				"%s:%d: CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: (*CachingSessionStore).%s: %s",
				rel, line, methodName, violation,
			))
		}
	})
	return out
}

// checkRevokeDelegateBody validates that body is exactly:
//
//	{ return <recvName>.inner.<methodName>(paramNames...) }
//
// recvName is the method's receiver variable name (e.g. "s").
// paramNames is the ordered list of method parameter ident names.
//
// Returns a non-empty violation description string on failure, "" on pass.
func checkRevokeDelegateBody(body *ast.BlockStmt, methodName string, recvName string, paramNames []string) string {
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
	// Fun must be a SelectorExpr: <something>.inner.<methodName>
	// We accept the outer selector as: selOuter.Sel.Name == methodName
	// and selOuter.X must be another SelectorExpr: <recv>.inner
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
	// step 8: innerSel.X must be an *ast.Ident whose Name matches the method receiver.
	recvIdent, ok := innerSel.X.(*ast.Ident)
	if !ok {
		return fmt.Sprintf("callee receiver is %T; want *ast.Ident (receiver variable)", innerSel.X)
	}
	if recvName != "" && recvIdent.Name != recvName {
		return fmt.Sprintf("callee receiver is %q; want method receiver %q", recvIdent.Name, recvName)
	}
	// step 9: arg count must match param count.
	if len(callExpr.Args) != len(paramNames) {
		return fmt.Sprintf("callee has %d arg(s); want %d (one per param)", len(callExpr.Args), len(paramNames))
	}
	// step 10: each arg must be a plain *ast.Ident matching the corresponding param name.
	// Index-based iteration avoids the "for-range []ast.Expr + type-assert" pattern
	// that SCANNER-FRAMEWORK-USAGE-01 Path-B prohibits in archtest files.
	i := 0
	mismatch := ""
	EachInChildren[ast.Ident](callExpr, func(argIdent *ast.Ident) {
		if mismatch != "" || i >= len(paramNames) {
			return
		}
		// EachInChildren visits all direct *ast.Ident children of callExpr,
		// which includes Fun idents. We only want Args; skip any Ident that
		// is the callee selector (Fun is a SelectorExpr, not a bare Ident here,
		// so direct Ident children of callExpr are exactly the args).
		// Double-check: if callExpr.Fun is *ast.SelectorExpr, its Ident parts
		// are NOT direct children of callExpr — they are children of the
		// SelectorExpr child. EachInChildren depth=1 is thus safe.
		if argIdent.Name != paramNames[i] {
			mismatch = fmt.Sprintf("callee arg[%d] is %q; want param ident %q", i, argIdent.Name, paramNames[i])
		}
		i++
	})
	if mismatch != "" {
		return mismatch
	}
	// Check for non-Ident args: if i < len(paramNames) after visiting all
	// direct Ident children, some args were non-Ident expressions.
	if i != len(paramNames) {
		return fmt.Sprintf("callee has %d plain-ident arg(s); want %d (some args are non-Ident expressions)", i, len(paramNames))
	}
	return ""
}

// verifyRevokeDelegateRedFixture loads fixtureName and asserts ≥ 1 violation
// is detected. Fixture load failure is hard-fail: a fixture that stops
// compiling silently disables the RED self-check.
func verifyRevokeDelegateRedFixture(t *testing.T, label, pattern string) {
	t.Helper()
	var found int
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			found += len(scanRevokeDelegateViolations(p, file, rel))
		}
		return nil
	})
	require.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture file has the correct deviation and is type-checkable.",
		label)
}

// ─── Blind-spot self-check tests ────────────────────────────────────────────

// TestCachingSessionRevokeDelegateOnly_BlindSpot_MultiStmt asserts that
// multi-statement Revoke/RevokeForSubject bodies are absent from production
// adapters/redis code (post-GREEN state). The F1 RED fixture (fixtures/
// caching_session_revoke_f1_multi_stmt/) proves the detection mechanism works:
// verifyRevokeDelegateRedFixture asserts ≥ 1 violation in that fixture.
// This blind-spot self-check asserts the continued absence of multi-stmt bodies
// in the real production code, closing the coverage loop.
func TestCachingSessionRevokeDelegateOnly_BlindSpot_MultiStmt(t *testing.T) {
	t.Parallel()
	// Verification via the F1 fixture: verifyRevokeDelegateRedFixture already
	// asserts that a multi-statement body (log.Print + return) is detected.
	// We additionally verify production absence explicitly.
	var multiStmtFound bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./adapters/redis/..."}, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if !revokeTargetMethods[fn.Name.Name] {
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
		"production Revoke/RevokeForSubject body must be single-statement after GREEN fix; F1 fixture is the RED-state mirror")
}

// TestCachingSessionRevokeDelegateOnly_BlindSpot_MethodValue asserts that
// method-value assignment of Revoke/RevokeForSubject (e.g. `fn := s.inner.Revoke`)
// does NOT appear in adapters/redis production code. If it did, a 2-stmt body
// (assign + return fn(...)) would catch it via the >1 statement check, but the
// callee-name check would be skipped. Documents the existing coverage guarantee.
func TestCachingSessionRevokeDelegateOnly_BlindSpot_MethodValue(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./adapters/redis/..."}, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if revokeTargetMethods[sel.Sel.Name] {
						line := p.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: method-value assignment of %s detected — blind spot for body-shape check",
							rel, line, sel.Sel.Name,
						))
					}
				})
			})
		}
		return nil
	})
	assert.Empty(t, violations,
		"CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 blind-spot: method-value assignment of "+
			"Revoke/RevokeForSubject found in adapters/redis production code — "+
			"refactor to direct call form so the body-shape archtest remains complete.")
}

// TestCachingSessionRevokeDelegateOnly_BlindSpot_Reflect asserts that
// reflect.MethodByName("Revoke") / ("RevokeForSubject") does NOT appear in
// adapters/redis production code, confirming the reflect blind spot is absent.
func TestCachingSessionRevokeDelegateOnly_BlindSpot_Reflect(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./adapters/redis/..."}, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "MethodByName" {
					return
				}
				if len(call.Args) != 1 {
					return
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return
				}
				name := strings.Trim(lit.Value, `"`)
				if revokeTargetMethods[name] {
					line := p.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.MethodByName(%q) detected — "+
							"archtest cannot see reflect-based invocations",
						rel, line, name,
					))
				}
			})
		}
		return nil
	})
	assert.Empty(t, violations,
		"CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 blind-spot: reflect.MethodByName of "+
			"Revoke/RevokeForSubject found in adapters/redis — refactor to direct form.")
}
