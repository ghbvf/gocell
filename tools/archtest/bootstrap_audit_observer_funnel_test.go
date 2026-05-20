// invariants:
//   - INVARIANT: BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01
//   - INVARIANT: BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01
//
// Funnel double-lock for BOOTSTRAP-AUDIT-CHAIN-WIRING-01 (plan 039 W1-2).
// runtime/auth.NewBootstrapMiddleware accepts a third positional argument of
// type auth.BootstrapAuthFailObserver (= func(ctx, reason)). Both the
// bootstrap-period slog channel AND the audit hash-chain channel must fire
// for every failure event; relying on a freestyle func literal here is what
// the prior bootstrapAuthFailLogger did and how the audit channel was lost.
//
// Downstream Hard: audit.NewBootstrapAuthFailObserver's RETURNED CLOSURE
// MUST call audit.AppendBootstrapAuthFail on the closure's unconditionally-
// executed path. The check is a 3-conjunction (all must hold simultaneously):
//
//  1. Typed callee identity: the CallExpr's Fun resolves (via ResolvePackageRef)
//     to runtime/audit.AppendBootstrapAuthFail in the canonical runtime/audit
//     package — same-named selectors from foreign packages or unrelated
//     function-typed values do NOT match.
//  2. Closure-body scope: the CallExpr lives inside the FuncLit literally
//     returned by the constructor (not the constructor body at large, where
//     dead code or sibling helpers could fake compliance with the simpler
//     "constructor body contains call" rule).
//  3. Unconditional-position scope: the CallExpr's position is NOT inside any
//     conditionally-executed scope of the closure body — IfStmt.Body /
//     IfStmt.Else / ForStmt.Body / RangeStmt.Body / SwitchStmt.Body /
//     SelectStmt.Body / TypeSwitchStmt.Body / nested FuncLit.Body all count
//     as "may not execute on every invocation". The production shape
//     `if err := AppendBootstrapAuthFail(...); err != nil { ... }` puts the
//     call in IfStmt.Init, which IS unconditional (the init clause runs
//     before the condition is evaluated); the error handler inside the if's
//     Body is excluded from the search.
//
// Together this rejects three bypass shapes that the prior "anywhere in the
// returned closure subtree" check would have passed:
//   - `if false { AppendBootstrapAuthFail(...) }` (call in IfStmt.Body — the
//     specific dead-code worry surfaced in PR #603 second-round review)
//   - `for cond { AppendBootstrapAuthFail(...) }` (call in ForStmt.Body —
//     zero iterations is a valid execution)
//   - `_ = func(){ AppendBootstrapAuthFail(...) }` or `defer func(){...}()`
//     with the inner closure never invoked (call in nested FuncLit.Body)
//
// AI-rebust rating (downstream): Hard up to AST-pattern reachability. The
// remaining theoretical blind spots — true CFG reachability (e.g. an
// unconditional `return` before the call), and helper-function indirection
// (`return func(...){ helperThatAppends(...) }` where helperThatAppends lives
// in a same-package FuncDecl) — require SSA-based reachability analysis
// (golang.org/x/tools/go/ssa BasicBlock walk, cf. honnef.co/go/tools `SA9003`
// empty-branch family). The upgrade is tracked as backlog
// BOOTSTRAP-AUDIT-OBSERVER-DOWNSTREAM-SSA-REACHABILITY-HARD-UPGRADE-01.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes — `proceed=false` push
// semantic at FuncLit / IfStmt.Body is the standard-library equivalent of
// the `collectClosureConditionalScopes` / `posInsideAnyRange` pair used here.
// ref: tools/archtest/cell_init_checknotnoop_test.go — same `funcLitRange` +
// `posInsideAnyRange` boundary-emulation pattern (CELL-L2-INIT-CHECKNOTNOOP-
// CALLED-01) is reused below.
//
// Upstream Medium (Object-bound + reassign-scanned): every production call to
// auth.NewBootstrapMiddleware in cmd/corebundle/ (non-test) must pass an
// observer that is either (a) the direct return value of a CallExpr resolving
// (via ResolvePackageRef) to runtime/audit.NewBootstrapAuthFailObserver, or
// (b) a *types.Object short-declared (:=) from such a CallExpr in the same
// source file AND never subsequently reassigned (=) to a non-funnel RHS.
// Tracking by types.Object — not bare ident name — defeats same-name shadow
// in another scope; the reassign scan defeats `obs = func(...){slog only}`
// shape that an earlier name-only check would miss.
//
// Wrapper-chain blind spot remains (e.g. `obs2 := wrap(obs1)` where wrap
// returns a custom impl): this is reachable only via Go-type-system upstream
// Hard (sealing auth.BootstrapAuthFailObserver into a marker that only
// runtime/audit can construct, which forces runtime/auth → runtime/audit,
// breaking the documented non-dependency). Promotion to Hard is tracked as
// backlog BOOTSTRAP-AUDIT-OBSERVER-RUNTIME-AUTH-WINDOW-01 (arm a: sealed
// marker, cap-14). This archtest defines the Medium ceiling reachable
// without that refactor.
//
// Scope carve-out (must match the F2 plan decision):
//   - examples/ssobff/app.go intentionally still wires the legacy
//     slog-only observer; backlog SSOBFF-BOOTSTRAP-AUDIT-CHAIN-WIRING-01
//     will move it through this same funnel and widen the scope below.
//   - cmd/corebundle/*_test.go invocations are mocks for rate-limit /
//     bootstrap-credential paths and do not represent production wiring;
//     RunTypedProduction(opts.Tests=false) keeps them out of the Pass set.

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleBootstrapAuditObserverFunnelDownstreamHard01 = "BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01"
	ruleBootstrapAuditObserverFunnelUpstreamMedium01 = "BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01"

	auditPkgSuffix          = "/runtime/audit"
	authPkgSuffix           = "/runtime/auth"
	corebundlePkgSuffix     = "/cmd/corebundle"
	observerFnName          = "NewBootstrapAuthFailObserver"
	appendFnName            = "AppendBootstrapAuthFail"
	bootstrapMiddlewareName = "NewBootstrapMiddleware"
)

// TestBootstrapAuditObserverFunnelDownstreamHard01 enforces the downstream
// half of the funnel: inside runtime/audit, the only constructor that
// produces a BootstrapAuthFailObserver — NewBootstrapAuthFailObserver — must
// (a) return a FuncLit closure as its observer value, (b) call the typed
// runtime/audit.AppendBootstrapAuthFail inside that closure's body, AND
// (c) place that call on the closure's unconditionally-executed path.
// Removing the call, hiding it in dead code outside the returned closure,
// shadowing it with a same-named selector from another package, or burying
// it inside a conditional/loop/nested-FuncLit branch would let the funnel
// emit observers that satisfy callers' types but silently double-write
// nothing to the ledger.
//
// Hard form (3-conjunction; see file-header doc for the full rationale and
// remaining SSA-reachability gap):
//  1. callee resolves via ResolvePackageRef to runtime/audit.AppendBootstrapAuthFail;
//  2. scope is the returned FuncLit body, not the constructor body at large;
//  3. position is NOT inside any conditionally-executed scope (IfStmt.Body /
//     Else, ForStmt.Body, RangeStmt.Body, SwitchStmt.Body, SelectStmt.Body,
//     TypeSwitchStmt.Body, nested FuncLit.Body).
//
// All three must hold simultaneously — any other shape fails archtest.
func TestBootstrapAuditObserverFunnelDownstreamHard01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix

	var (
		constructorBody *ast.BlockStmt
		funcLocation    string
		passRef         *Pass
	)
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != auditPkgPath {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != observerFnName {
					return
				}
				constructorBody = fn.Body
				funcLocation = p.Rel(file) + ":" + p.Fset.Position(fn.Pos()).String()
				passRef = p
			})
		}
		return nil
	})

	require.NotNil(t, constructorBody,
		"%s: runtime/audit must export %s as the single observer constructor",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName)
	require.NotNil(t, passRef, "%s: typed Pass must be captured", ruleBootstrapAuditObserverFunnelDownstreamHard01)

	// Find the returned FuncLit. The Hard form requires the AppendBootstrapAuthFail
	// call to live inside the closure that is actually handed back to callers,
	// not anywhere else in the constructor body (where dead code or sibling
	// helpers could fake compliance with the simpler "body contains call" rule).
	//
	// Outer EachInSubtree + done sentinel mirrors the FINDFIRSTINSUBTREE-API-01
	// gap (no typed find-first-in-subtree helper yet). Inner FindFirstChild is
	// the scanner-framework-approved depth-1 find-first funnel (USAGE-02):
	// ReturnStmt's direct children are its Results []ast.Expr, so a FuncLit
	// returned positionally appears as a depth-1 child here. This shape also
	// closes SCANNER-FRAMEWORK-USAGE-01 Path B (no for-range over []ast.Expr
	// with type assertion).
	var returnedClosureBody *ast.BlockStmt
	EachInSubtree[ast.ReturnStmt](constructorBody, func(ret *ast.ReturnStmt) {
		if returnedClosureBody != nil {
			return
		}
		fl, ok := FindFirstChild[ast.FuncLit](ret, func(*ast.FuncLit) bool { return true })
		if ok {
			returnedClosureBody = fl.Body
		}
	})
	require.NotNil(t, returnedClosureBody,
		"%s: %s must return a FuncLit closure as its observer; non-FuncLit return shape at %s "+
			"would break the closure-body scope this Hard rule depends on",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName, funcLocation)

	// Pre-collect every conditional-execution scope inside the returned
	// closure body. The CallExpr walk below filters out any candidate whose
	// position falls inside one of these ranges, leaving only calls on the
	// closure's unconditionally-executed path. This is the third leg of the
	// Hard 3-conjunction described in the test godoc.
	conditionalRanges := collectClosureConditionalScopes(returnedClosureBody)

	// Typed callee resolution: callee must resolve to the canonical
	// runtime/audit.AppendBootstrapAuthFail (both bare-ident same-package
	// calls and external SelectorExpr qualified calls work via ResolvePackageRef).
	// Same-named selectors from foreign packages or unrelated function-typed
	// values will NOT match.
	var found bool
	EachInSubtree[ast.CallExpr](returnedClosureBody, func(call *ast.CallExpr) {
		if found {
			return
		}
		if posInsideAnyRange(call.Pos(), conditionalRanges) {
			return // call lives in a conditional/loop/nested-FuncLit branch
		}
		pkgPath, name, ok := ResolvePackageRef(passRef.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if pkgPath == auditPkgPath && name == appendFnName {
			found = true
		}
	})
	assert.True(t, found,
		"%s: returned closure body of %s must invoke typed %s.%s on the "+
			"unconditionally-executed path — calls buried in IfStmt.Body, "+
			"loops, or nested FuncLits do NOT count; otherwise the observer "+
			"never reaches the audit ledger and downgrades back to slog-only (%s)",
		ruleBootstrapAuditObserverFunnelDownstreamHard01,
		observerFnName, auditPkgPath, appendFnName, funcLocation)
}

// collectClosureConditionalScopes returns the position spans of every AST
// node inside root that represents a conditionally-executed scope — i.e. a
// scope that may NOT execute on every invocation of the enclosing function.
//
// Scopes included (start = scope-body start, end = scope-body end):
//   - IfStmt.Body and IfStmt.Else (only one branch runs per execution, and
//     literal `if false { ... }` runs neither — the user's specific dead-code
//     worry from PR #603 second-round review)
//   - ForStmt.Body and RangeStmt.Body (zero iterations is a valid execution)
//   - SwitchStmt.Body / TypeSwitchStmt.Body / SelectStmt.Body (case selection
//     is dynamic; no single case is guaranteed to run, and an empty switch
//     runs no case at all)
//   - FuncLit.Body (a nested closure only runs if explicitly invoked — the
//     `_ = func(){ ... }` / `defer func(){ ... }()` with the inner closure
//     never invoked bypass)
//
// Scopes deliberately NOT included (always evaluated when the enclosing
// function reaches the statement):
//   - IfStmt.Init and IfStmt.Cond — `if err := X(); err != nil` puts X on
//     the unconditional path; this is the production shape used by
//     audit.NewBootstrapAuthFailObserver and must keep passing
//   - ForStmt.Init / Cond / Post — evaluated even when the loop body runs zero times
//   - SwitchStmt.Init / Tag — evaluated unconditionally
//   - DeferStmt.Call — the call expression's arguments are evaluated at the
//     defer statement (unconditional); the deferred callee runs at function
//     exit (also unconditional given that the deferring statement was reached)
//
// Ranges use *ast.BlockStmt position spans where available (matching the
// existing `funcLitRange` helper from cell_init_checknotnoop_test.go); for
// IfStmt.Else (which can be either a BlockStmt or another IfStmt for `else if`
// chains) the raw node span is used.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes proceed=false on FuncLit /
// IfStmt.Body push events — same boundary semantic, emulated here on top of
// EachInSubtree because the archtest scanner does not yet expose a
// boundary-controlled walker (tracked as ARCHTEST-WALKER-BOUNDARY-CONTROL-01).
func collectClosureConditionalScopes(root ast.Node) []funcLitRange {
	var ranges []funcLitRange
	addBlock := func(b *ast.BlockStmt) {
		if b == nil {
			return
		}
		ranges = append(ranges, funcLitRange{start: b.Pos(), end: b.End()})
	}
	// Seven typed traversals instead of one ast.Inspect: required by
	// SCANNER-FRAMEWORK-USAGE-01 (no bare go/ast.Inspect from archtest test
	// files; use scanner-framework EachInSubtree[N] funnels). Closure bodies
	// are small enough that the constant-factor cost is negligible.
	EachInSubtree[ast.IfStmt](root, func(s *ast.IfStmt) {
		addBlock(s.Body)
		if s.Else != nil {
			ranges = append(ranges, funcLitRange{start: s.Else.Pos(), end: s.Else.End()})
		}
	})
	EachInSubtree[ast.ForStmt](root, func(s *ast.ForStmt) { addBlock(s.Body) })
	EachInSubtree[ast.RangeStmt](root, func(s *ast.RangeStmt) { addBlock(s.Body) })
	EachInSubtree[ast.SwitchStmt](root, func(s *ast.SwitchStmt) { addBlock(s.Body) })
	EachInSubtree[ast.TypeSwitchStmt](root, func(s *ast.TypeSwitchStmt) { addBlock(s.Body) })
	EachInSubtree[ast.SelectStmt](root, func(s *ast.SelectStmt) { addBlock(s.Body) })
	EachInSubtree[ast.FuncLit](root, func(s *ast.FuncLit) { addBlock(s.Body) })
	return ranges
}

// TestCollectClosureConditionalScopes is the reverse self-check for the
// helper that powers the third leg of BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-
// DOWNSTREAM-HARD-01's conjunction. ai-collab.md §"工具选定后强制盲区自检"
// requires every Hard/Medium rule to ship a reverse test asserting the chosen
// walker actually catches the AST shapes the godoc claims it catches.
//
// The fixtures below parse synthetic Go snippets and assert that calls inside
// each declared conditional shape ARE filtered out (posInsideAnyRange returns
// true) while calls on the unconditional path (IfStmt.Init / top-level
// ExprStmt) are NOT filtered. If a future archtest refactor breaks the helper,
// this test fails before the funnel rule silently degrades to "anywhere in
// closure subtree".
func TestCollectClosureConditionalScopes(t *testing.T) {
	t.Parallel()

	parseClosureBody := func(t *testing.T, src string) (*ast.BlockStmt, []*ast.CallExpr) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", src, parser.AllErrors)
		require.NoError(t, err, "parse synthetic source")
		// EachInSubtree + done-sentinel (FINDFIRSTINSUBTREE-API-01 gap, no
		// typed find-first-in-subtree helper yet). Both walks here are over
		// already-collected synthetic AST so there is no live-codebase scope
		// — they use the same scanner funnel as production rules for
		// SCANNER-FRAMEWORK-USAGE-01 consistency.
		var body *ast.BlockStmt
		EachInSubtree[ast.FuncLit](f, func(fl *ast.FuncLit) {
			if body != nil {
				return
			}
			body = fl.Body
		})
		require.NotNil(t, body, "synthetic source must contain a FuncLit")
		var calls []*ast.CallExpr
		EachInSubtree[ast.CallExpr](body, func(c *ast.CallExpr) {
			id, ok := c.Fun.(*ast.Ident)
			if !ok || id.Name != "target" {
				return
			}
			calls = append(calls, c)
		})
		return body, calls
	}

	t.Run("unconditional positions stay outside conditional ranges", func(t *testing.T) {
		t.Parallel()
		const src = `package p
var _ = func() {
	target("top-level ExprStmt")           // 1: pure top-level
	x := target("AssignStmt.Rhs")          // 2: top-level assignment
	_ = x
	if err := target("IfStmt.Init"); err != nil { // 3: production shape
		_ = err
	}
}`
		body, calls := parseClosureBody(t, src)
		require.Len(t, calls, 3, "fixture should declare 3 unconditional calls")
		ranges := collectClosureConditionalScopes(body)
		for i, c := range calls {
			assert.Falsef(t, posInsideAnyRange(c.Pos(), ranges),
				"call #%d %q must NOT be inside any conditional scope (unconditional production shape)",
				i+1, exprStringForLog(c))
		}
	})

	t.Run("conditional positions are inside collected ranges", func(t *testing.T) {
		t.Parallel()
		const src = `package p
var _ = func() {
	if false {
		target("IfStmt.Body — user's PR #603 dead-code worry")
	} else {
		target("IfStmt.Else")
	}
	for i := 0; i < 0; i++ {
		target("ForStmt.Body")
	}
	for _, x := range []int{} {
		_ = x
		target("RangeStmt.Body")
	}
	switch 1 {
	case 1:
		target("SwitchStmt.Body")
	}
	_ = func() { target("nested FuncLit.Body") } // never invoked
}`
		body, calls := parseClosureBody(t, src)
		require.Len(t, calls, 6, "fixture should declare 6 conditional/nested calls")
		ranges := collectClosureConditionalScopes(body)
		for i, c := range calls {
			assert.Truef(t, posInsideAnyRange(c.Pos(), ranges),
				"call #%d %q must be inside a conditional scope (bypass shape)",
				i+1, exprStringForLog(c))
		}
	})
}

// exprStringForLog renders a CallExpr's first string-literal argument for
// readable test diagnostics; falls back to "<call>" for unexpected shapes.
func exprStringForLog(c *ast.CallExpr) string {
	if len(c.Args) == 0 {
		return "<call>"
	}
	if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		if s, ok2 := StringLitValue(lit); ok2 {
			return s
		}
	}
	return "<call>"
}

// TestBootstrapAuditObserverFunnelUpstreamMedium01 enforces the upstream
// half: every production call to auth.NewBootstrapMiddleware in cmd/corebundle
// must hand the funnel-built observer as its third positional argument. The
// observer may be either the direct CallExpr to
// audit.NewBootstrapAuthFailObserver or an identifier short-declared from it
// in the same source file.
//
// Why a small Medium gap exists: a function-level "X = func(ctx, reason){...}"
// assignment with the same identifier as a legitimate funnel-built observer
// could in principle smuggle a slog-only impl past this check. Hardening
// requires sealing auth.BootstrapAuthFailObserver (see
// BOOTSTRAP-AUDIT-OBSERVER-RUNTIME-AUTH-WINDOW-01 arm a backlog entry); the
// archtest defines the Medium ceiling reachable without that refactor.
func TestBootstrapAuditObserverFunnelUpstreamMedium01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix
	authPkgPath := modPath + authPkgSuffix
	corebundlePkgPath := modPath + corebundlePkgSuffix

	var upstreamViolations []upstreamViolation

	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != corebundlePkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			observerObjects := observerObjectsInFile(p, file, auditPkgPath)
			// Reassign scan: a funnel-built object subsequently rebound to a
			// non-funnel RHS (FuncLit, wrapper-returned closure, …) loses the
			// audit-chain guarantee. The reassign violation is reported even
			// if the rebound object never reaches NewBootstrapMiddleware,
			// because the shape itself is what the funnel forbids.
			upstreamViolations = append(upstreamViolations,
				reassignViolations(p, file, observerObjects, auditPkgPath)...)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok || pkgPath != authPkgPath || name != bootstrapMiddlewareName {
					return
				}
				if len(call.Args) < 3 {
					upstreamViolations = append(upstreamViolations, upstreamViolation{
						location: rel + ":" + p.Fset.Position(call.Pos()).String(),
						reason:   "fewer than 3 positional args; expected (creds, limiter, observer)",
					})
					return
				}
				arg := call.Args[2]
				if isAuditObserverConstructorCall(p, arg, auditPkgPath) {
					return
				}
				if id, ok := arg.(*ast.Ident); ok {
					if obj := p.TypesInfo.Uses[id]; obj != nil && observerObjects[obj] {
						return
					}
				}
				upstreamViolations = append(upstreamViolations, upstreamViolation{
					location: rel + ":" + p.Fset.Position(arg.Pos()).String(),
					reason:   "third arg must be audit.NewBootstrapAuthFailObserver(...) or a *types.Object short-declared from it (never reassigned)",
				})
			})
		}
		return nil
	})

	for _, v := range upstreamViolations {
		t.Logf("%s: %s — %s", ruleBootstrapAuditObserverFunnelUpstreamMedium01, v.location, v.reason)
	}
	assert.Empty(t, upstreamViolations,
		"%s: cmd/corebundle production callers of auth.%s must route through audit.%s; "+
			"recovering to slog-only observers (the pre-PR shape) is what this rule prevents",
		ruleBootstrapAuditObserverFunnelUpstreamMedium01, bootstrapMiddlewareName, observerFnName)
}

// upstreamViolation is the upstream Medium scan unit: location + reason for one
// archtest finding inside cmd/corebundle production files.
type upstreamViolation struct {
	location string
	reason   string
}

// observerObjectsInFile returns the set of *types.Object that were
// short-declared (:=) from a CallExpr to audit.NewBootstrapAuthFailObserver
// anywhere in file. Object-keyed (not name-keyed) so same-name shadows in
// other scopes do not contaminate the funnel-registered set.
//
// Only token.DEFINE (`:=`) registers an object; bare `=` reassignment is
// scanned separately by reassignViolations to detect funnel-built observers
// later rebound to non-funnel RHS.
func observerObjectsInFile(p *Pass, file *ast.File, auditPkgPath string) map[types.Object]bool {
	out := map[types.Object]bool{}
	EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
		if stmt.Tok != token.DEFINE || len(stmt.Rhs) != 1 || len(stmt.Lhs) == 0 {
			return
		}
		if !isAuditObserverConstructorCall(p, stmt.Rhs[0], auditPkgPath) {
			return
		}
		id, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok || id.Name == "_" {
			return
		}
		if obj := p.TypesInfo.Defs[id]; obj != nil {
			out[obj] = true
		}
	})
	return out
}

// reassignViolations finds AssignStmt with token.ASSIGN (bare `=`) where the
// LHS resolves to an object already registered by observerObjectsInFile AND
// the RHS is NOT another funnel call. This catches the upstream Medium
// blind-spot from earlier name-only checks:
//
//	obs, _ := audit.NewBootstrapAuthFailObserver(...) // registered
//	obs    = func(ctx, reason){ /* slog only */ }     // ← caught here
//	auth.NewBootstrapMiddleware(creds, lim, obs)       // would otherwise smuggle
//
// Reassigning a funnel observer to ANY non-funnel RHS is a upstreamViolation in
// itself, regardless of whether the rebound object reaches NewBootstrapMiddleware.
func reassignViolations(p *Pass, file *ast.File, observerObjects map[types.Object]bool, auditPkgPath string) []upstreamViolation {
	var out []upstreamViolation
	EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
		if stmt.Tok != token.ASSIGN || len(stmt.Rhs) != 1 || len(stmt.Lhs) == 0 {
			return
		}
		id, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok {
			return
		}
		obj := p.TypesInfo.Uses[id]
		if obj == nil || !observerObjects[obj] {
			return
		}
		if isAuditObserverConstructorCall(p, stmt.Rhs[0], auditPkgPath) {
			return
		}
		out = append(out, upstreamViolation{
			location: p.Rel(file) + ":" + p.Fset.Position(stmt.Pos()).String(),
			reason:   "funnel-built observer reassigned to non-funnel RHS (would bypass audit hash-chain)",
		})
	})
	return out
}

// isAuditObserverConstructorCall reports whether expr is a CallExpr whose
// callee resolves to audit.NewBootstrapAuthFailObserver in the canonical
// runtime/audit package — alias-resistant via typed *types.PkgName lookup.
func isAuditObserverConstructorCall(p *Pass, expr ast.Expr, auditPkgPath string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok {
		return false
	}
	return pkgPath == auditPkgPath && name == observerFnName
}
