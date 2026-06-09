package archtest

// taggroup_loop_no_typed_run_invariants.go — importable TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01
// rule logic.
//
// TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01 — backstory and form-uniqueness
//
// Backstory: panic_invariants_test.go::TestPanicRegistered once wrote the
// pattern (shown in its pre-#1037-§1d RunTyped form; RunTyped was deleted when
// the 5 Run* entries collapsed into a single Run + sealed RunScope, so the
// modern equivalent is Run(t, Typed(TypedOpts{Tags: tagGroup}, []string{"./..."}), scan)):
//
//	for _, tagGroup := range KnownNonDefaultTags() {
//	    _ = RunTyped(t, TypedOpts{Tags: tagGroup}, []string{"./..."}, scan)
//	}
//
// Each tagGroup is a distinct typeseval.SharedResolver cacheKey, so the loop
// drives N separate packages.Load NeedTypesInfo calls on the same shard. The
// cumulative RSS overshoots GHA 2-CPU 7GB shard 12/14: R2-P3 PR-b #530 CI
// observed OOM SIGTERM and a 43s wall on TestNotFoundTestStrict that had
// copied the pattern. Fresh-instance Claude reading the panic_invariants
// fixture will re-copy the shape; this rule makes the re-copy fail in CI.
//
// Hard form-uniqueness (callee, body) — pairs with ai-robust.md §"Hard 范本"
// 第 2 条 (panic register Approved):
//
//	(i) RangeStmt.X resolves via *types.Info to typeseval.KnownNonDefaultTags,
//	    either as a direct *ast.CallExpr (covers archtest re-export, direct
//	    typeseval import, and aliased import — all lower to the same
//	    *types.Func identity) OR as an *ast.Ident bound — within the same
//	    FuncDecl/FuncLit — by `:=` / `=` / `var` from such a CallExpr (the
//	    var-indirection form; see blind-spot closure BS-4 below).
//	(ii) RangeStmt.Body, walked recursively, must contain an archtest.Run
//	     CallExpr whose RunScope arg (2nd positional) is a typed scope —
//	     Typed/Production/Fixture/StandaloneModule — either as a direct
//	     constructor CallExpr OR as an *ast.Ident bound to such a call (the
//	     scope-var-indirection form; see blind-spot closure BS-6 below).
//
// Both conditions AND → violation. Either side alone is fine: a tagGroup
// loop that only inspects the slice (no typed-scope Run) is OK — this is a
// real legitimate idiom: pass_test.go binds KnownNonDefaultTags() to a var for
// façade↔oracle parity assertion (no range, no Run); a typed-scope Run call
// outside a tagGroup loop is OK.
//
// Compliant idioms (see fixture green_*):
//   - single Run(t, Typed(TypedOpts{Tags: FlatNonDefaultTags()}, ...), ...)
//   - two Run(t, Typed(...), ...) calls: tags=nil + tags=FlatNonDefaultTags()
//     with shared seen-map dedup (defensive: covers reverse build directives
//     //go:build !X which are silently excluded from a -tags=...,X,... union load)
//
// AI-robust grade: Hard (typed-function-call funnel with (callee, body)
// double-factor form-uniqueness). Same shape and termination criteria as
// PANIC-REGISTERED-01 — there is no "looks-like-KnownNonDefaultTags but
// isn't" gray zone because the identity check goes through *types.Info.
//
// Scope: tools/archtest/*_test.go (direct children only — fixtures under
// tools/archtest/internal/taggrouploopfixtures/ are excluded from the live
// scan and exercised by Test_TaggroupLoopFixturePrecisionGate in
// taggroup_loop_no_typed_run_test.go).
//
// Blind-spot self-checks (ai-robust.md §"工具选定后强制盲区自检").
//
// rangeExprCallsKnownNonDefaultTags / bodyContainsTypedRun resolve callee
// identity through *types.Info — staticcheck SA4000 style (direct AST +
// types.Info form-matching, no value-flow chasing). The forms below are
// outside that resolver's declared range; each is either CLOSED with a RED
// fixture or an ACCEPTED blind spot with rationale.
//
//	BS-1 Façade vs internal callee (CLOSED): fixtures exercise both
//	   archtest.KnownNonDefaultTags (façade re-export in resolve.go) and
//	   typeseval.KnownNonDefaultTags (direct internal import). Both point at
//	   the same *types.Func object — covered by red_panic_invariants_style.go
//	   and red_typeseval_qualified.go. Alias import form (kt "…/typeseval")
//	   also resolves to the same *types.Func identity — red_aliased_import.go.
//	BS-2 Nested-closure body (CLOSED): a typed-scope Run invoked inside an IIFE
//	   within the loop body must still be caught (subtree walk, not direct-child
//	   walk). Covered by red_nested_closure.go.
//	BS-3 Patterns variance (CLOSED): the loop-amortization invariant is
//	   independent of the patterns arg shape; subpath patterns like
//	   ./corecells/... must also be caught when wrapped in a tagGroup loop.
//	   Covered by red_subpath_typed_run.go.
//	BS-4 RangeStmt.X var-indirection (CLOSED): `tags := KnownNonDefaultTags();
//	   for _, g := range tags { Run(t, Typed(...), ...) }`. condition (i) now
//	   also matches an *ast.Ident bound from KnownNonDefaultTags() within the
//	   same FuncDecl/FuncLit (collectKnownTagsBoundObjects). Precision is proven
//	   both directions: red_var_bound_range.go (bind→range→typed-scope Run →
//	   caught) and green_var_bound_parity.go (bind→len-assert, no range/Run, the
//	   pass_test.go façade-parity idiom → NOT caught, no false positive).
//	   Narrow accepted sub-gap: only single-binding (`tags := f()` /
//	   `var tags = f()`) is recognized; multi-RHS positional binding
//	   (`a, tags := x, f()`) is not — it is not a copy template a fresh
//	   instance reproduces, and single-element indexed access keeps the
//	   collector compliant with SCANNER-FRAMEWORK-USAGE-01 (no for-range +
//	   type-assert over []ast.Expr).
//	BS-5 Helper-wrapped typed-scope Run in loop body (ACCEPTED blind spot): a
//	   loop body that calls a package-local helper which itself calls a
//	   typed-scope Run is not caught — bodyContainsTypedRun resolves the direct
//	   callee only and does not perform intra-package call-graph reachability.
//	   Rationale:
//	   (a) closing it requires one-level (or recursive) call-graph analysis,
//	   disproportionate to a rule whose purpose is anti-copy of one specific
//	   historical template — the template a fresh-instance Claude reproduces
//	   is the *direct* typed-scope-Run-in-body shape (the now-removed
//	   panic_invariants loop / the red fixtures), not a helper-wrapped one;
//	   (b) a precise reverse self-check is infeasible —
//	   `func foo(){ Run(t, Typed(...), ...) }` local helpers are a pervasive
//	   legitimate idiom across the archtest suite, so any blanket "no local
//	   helper reaches a typed-scope Run" assertion would false-positive (the
//	   same accepted trade-off staticcheck SA4000 documents for fn()==fn()
//	   side-effect blindness).
//	   Upgrade path: one-level call-graph analysis; trigger =
//	   first real event of a helper-wrapped tagGroup loop reaching CI.
//	BS-6 RunScope-arg var-indirection (CLOSED): `scope := Typed(...);
//	   for ... { Run(t, scope, ...) }`. condition (ii) now also matches a Run
//	   whose 2nd arg is an *ast.Ident bound (file-level, single binding) to a
//	   typed-scope constructor call (collectTypedScopeBoundObjects /
//	   argIsTypedScope). Covered by red_scope_var_indirection.go. Same narrow
//	   accepted sub-gap as BS-4 (single-binding only; multi-RHS / cross-func /
//	   cross-file escape not traced) — both are the SCANNER-FRAMEWORK-USAGE-01
//	   single-element-binding accept.
//
// Reverse fixture-precision self-check (Test_TaggroupLoopFixturePrecisionGate in
// taggroup_loop_no_typed_run_test.go) enforces that GREEN fixtures (single
// Run(t, Typed(TypedOpts{Tags: FlatNonDefaultTags()}, ...), ...), two-load
// nil+FlatNonDefaultTags, and the BS-4 parity binding) do NOT trip the rule.

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	taggroupLoopRuleID         = "TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01"
	taggroupLoopKnownTagsName  = "KnownNonDefaultTags"
	taggroupLoopRunName        = "Run"
	taggroupLoopArchtestPkg    = PlatformModulePath + "/tools/archtest"
	taggroupLoopTypesevalPkg   = PlatformModulePath + "/tools/archtest/internal/typeseval"
	taggroupLoopFixtureDirSeg  = "tools/archtest/internal/taggrouploopfixtures/"
	taggroupLoopPatternProd    = "./tools/archtest/..."
	taggroupLoopPatternFixture = "./tools/archtest/internal/taggrouploopfixtures/..."
)

// taggroupTypedScopeCtors is the set of archtest typed-scope constructor names
// whose result, when handed to Run inside a KnownNonDefaultTags loop, drives one
// typeseval.SharedResolver packages.Load per tag group — the OOM-risking shape
// this rule forbids. The AST scope ([AST]) is intentionally excluded: AST-only
// dispatch is a cheap parser pass with no per-tag packages.Load amortization
// concern.
var taggroupTypedScopeCtors = map[string]struct{}{
	"Typed":            {},
	"Production":       {},
	"Fixture":          {},
	"StandaloneModule": {},
}

// CheckTagGroupLoopForbidsTypedRun runs TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01
// against tools/archtest/*_test.go (direct children only) and returns its
// diagnostics. GoCell's TestTagGroupLoopForbidsTypedRun calls this directly —
// single source, no parallel rule body. cfg.BuildTags is threaded into the scan's
// TypedOpts.Tags so files behind build directives are not missed.
//
// NOTE: This is a META rule — it governs how the archtest package itself uses
// typed-scope Run and KnownNonDefaultTags, so its scan scope is internal to GoCell
// (tools/archtest). It is intentionally NOT added to StandardCellRules() because
// external Cell repos do not contain archtest code; the rule would be vacuously
// green (empty scan set) and provide no value to external consumers. The symbol
// is exported only so the rule library is uniformly addressable — an external
// repo that calls it directly is likewise vacuously green, since the scan target
// ./tools/archtest/... does not exist outside GoCell. cfg is consumed for
// BuildTags only; the scan scope is fixed to GoCell's own archtest package.
func CheckTagGroupLoopForbidsTypedRun(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(TypedOpts{Tests: true, Tags: cfg.BuildTags}, []string{taggroupLoopPatternProd}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if !isLiveTaggroupTarget(rel) {
					continue
				}
				out = append(out, scanFileForTaggroupViolations(p, file, rel)...)
			}
			return out
		})
}

// scanFileForTaggroupViolations walks file looking for RangeStmt nodes whose
// range expression calls KnownNonDefaultTags AND whose body subtree contains
// a Run call with a typed scope (Typed/Production/Fixture/StandaloneModule).
// Uses [EachInSubtree] (the only allowed walk path per
// SCANNER-FRAMEWORK-USAGE-01) for both the outer RangeStmt enumeration and
// the inner CallExpr search.
func scanFileForTaggroupViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	boundObjs := collectKnownTagsBoundObjects(p, file)
	scopeBoundObjs := collectTypedScopeBoundObjects(p, file)
	EachInSubtree[ast.RangeStmt](file, func(rs *ast.RangeStmt) {
		if !rangeExprCallsKnownNonDefaultTags(p, rs.X, boundObjs) {
			return
		}
		bodyHit, hitLine := bodyContainsTypedRun(p, rs.Body, scopeBoundObjs)
		if !bodyHit {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: hitLine,
			Message: fmt.Sprintf(
				"for-range over %s with a typed-scope Run "+
					"(Run(t, Typed/Production/Fixture/StandaloneModule(...), ...)) inside loop body — "+
					"replace with single Run(t, Typed(TypedOpts{Tags: archtest.FlatNonDefaultTags()}, ...)) "+
					"or two-load nil+FlatNonDefaultTags pattern "+
					"(see ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md;"+
					" FlatNonDefaultTags defined in tools/archtest/resolve.go)",
				taggroupLoopKnownTagsName,
			),
		})
	})
	return out
}

// rangeExprCallsKnownNonDefaultTags reports whether expr is (a) a direct
// CallExpr whose Fun resolves via *types.Info to KnownNonDefaultTags in
// either archtest (façade re-export) or typeseval (internal), or (b) an
// *ast.Ident whose declared object is in boundObjs — the BS-4 var-indirection
// form `tags := KnownNonDefaultTags(); for _, g := range tags`.
func rangeExprCallsKnownNonDefaultTags(p *Pass, expr ast.Expr, boundObjs map[types.Object]struct{}) bool {
	if call, ok := expr.(*ast.CallExpr); ok {
		return callResolvesToKnownNonDefaultTags(p, call)
	}
	if id, ok := expr.(*ast.Ident); ok {
		if obj := taggroupObjectOf(p.TypesInfo, id); obj != nil {
			_, hit := boundObjs[obj]
			return hit
		}
	}
	return false
}

// callResolvesToKnownNonDefaultTags reports whether call.Fun resolves via
// *types.Info to KnownNonDefaultTags in the archtest façade or typeseval.
func callResolvesToKnownNonDefaultTags(p *Pass, call *ast.CallExpr) bool {
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || name != taggroupLoopKnownTagsName {
		return false
	}
	return pkgPath == taggroupLoopArchtestPkg || pkgPath == taggroupLoopTypesevalPkg
}

// collectKnownTagsBoundObjects returns the set of types.Object that are bound
// — anywhere in file via a single `:=`, `=`, or `var` — to a CallExpr
// resolving to KnownNonDefaultTags. This closes BS-4: the var-indirection
// form. Scope is file-level (not whole-program) because the bypass that
// matters — a tagGroup loop copied from the historical template — declares
// the binding in the same file as the loop.
//
// Only the single-binding shape `tags := KnownNonDefaultTags()` /
// `var tags = KnownNonDefaultTags()` is recognized: that is the realistic
// var-indirection copy template (and the shape of the pass_test.go
// façade-parity idiom the green fixture guards). Multi-RHS positional binding
// (`a, tags := x, KnownNonDefaultTags()`) is a narrow accepted sub-gap of
// BS-4 — not a copy template a fresh instance reproduces, and accessing the
// element by index avoids reimplementing a tree walk over []ast.Expr
// (SCANNER-FRAMEWORK-USAGE-01: no for-range + type-assert over an expr slice;
// single-element indexed access is the sanctioned idiom).
func collectKnownTagsBoundObjects(p *Pass, file *ast.File) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})
	collectTagsBoundFromAssignStmts(p, file, out)
	collectTagsBoundFromValueSpecs(p, file, out)
	return out
}

// collectTagsBoundFromAssignStmts populates dst with objects bound to
// KnownNonDefaultTags() via `:=` or plain `=` assignment statements.
func collectTagsBoundFromAssignStmts(p *Pass, file *ast.File, dst map[types.Object]struct{}) {
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || !callResolvesToKnownNonDefaultTags(p, call) {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			taggroupBindIdent(p.TypesInfo, id, dst)
		}
	})
}

// collectTagsBoundFromValueSpecs populates dst with objects bound to
// KnownNonDefaultTags() via `var` declarations.
func collectTagsBoundFromValueSpecs(p *Pass, file *ast.File, dst map[types.Object]struct{}) {
	EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		if len(vs.Names) != 1 || len(vs.Values) != 1 {
			return
		}
		call, ok := vs.Values[0].(*ast.CallExpr)
		if !ok || !callResolvesToKnownNonDefaultTags(p, call) {
			return
		}
		taggroupBindIdent(p.TypesInfo, vs.Names[0], dst)
	})
}

// taggroupBindIdent records the types.Object for id into dst, skipping blank
// identifiers and nil.
func taggroupBindIdent(info *types.Info, id *ast.Ident, dst map[types.Object]struct{}) {
	if id == nil || id.Name == "_" {
		return
	}
	if obj := taggroupObjectOf(info, id); obj != nil {
		dst[obj] = struct{}{}
	}
}

// taggroupObjectOf returns the types.Object an identifier refers to, checking
// Defs (declaration site, e.g. LHS of `:=` / `var`) then Uses (reference
// site, e.g. range expression or LHS of plain `=`).
//
// Also used (same package) by pass_funnel_test.go's fixture-tag var-indirection
// check — keep this resolver stable when renaming; a rename silently breaks that
// caller (both files are package archtest, so the compiler still links).
func taggroupObjectOf(info *types.Info, id *ast.Ident) types.Object {
	if obj := info.Defs[id]; obj != nil {
		return obj
	}
	return info.Uses[id]
}

// bodyContainsTypedRun walks body recursively and reports whether any
// descendant CallExpr is archtest.Run whose RunScope argument (2nd positional)
// is a typed-scope, in either form: (a) a direct CallExpr resolving to one of
// the typed-scope constructors in taggroupTypedScopeCtors
// (Typed/Production/Fixture/StandaloneModule), or (b) an *ast.Ident bound
// (file-level) to such a call — the scope-var-indirection form
// `scope := Typed(...); Run(t, scope, ...)` (F2). Uses FindFirstInSubtree, so it
// stops at the FIRST matching Run CallExpr (preorder) and returns its line
// number — one offending Run is enough to flag the enclosing loop body. The
// find-first scanner walker keeps the rule honest about scanner usage
// (SCANNER-FRAMEWORK-USAGE-02 sanctions FindFirstInSubtree for this early-return
// shape rather than an EachInSubtree + done-sentinel hand-roll).
//
// Both callee resolutions go through *types.Info (ResolvePackageRef), so the
// qualified / dot-import / aliased forms of archtest.Run AND of the scope
// constructor all lower to the same *types.Func identity — there is no
// "looks-like-Typed but isn't" gray zone. The AST scope ([AST]) is excluded by
// construction (it is absent from taggroupTypedScopeCtors): AST dispatch has no
// per-tag packages.Load amortization concern.
func bodyContainsTypedRun(p *Pass, body *ast.BlockStmt, scopeBoundObjs map[types.Object]struct{}) (bool, int) {
	call, ok := FindFirstInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !resolved || name != taggroupLoopRunName || pkgPath != taggroupLoopArchtestPkg {
			return false
		}
		if len(call.Args) < 2 {
			return false
		}
		return argIsTypedScope(p, call.Args[1], scopeBoundObjs)
	})
	if !ok {
		return false, 0
	}
	return true, p.Fset.Position(call.Pos()).Line
}

// argIsTypedScope reports whether the RunScope argument arg passed to
// archtest.Run is a typed-scope: (a) a direct CallExpr resolving to a typed-scope
// constructor in taggroupTypedScopeCtors, or (b) an *ast.Ident bound (file-level)
// to such a call, recorded in scopeBoundObjs by collectTypedScopeBoundObjects
// (the F2 scope-var-indirection form). Both legs resolve callee identity via
// *types.Info, so alias / dot-import forms collapse to the same *types.Func.
func argIsTypedScope(p *Pass, arg ast.Expr, scopeBoundObjs map[types.Object]struct{}) bool {
	if exprIsTypedScopeCtorCall(p, arg) {
		return true
	}
	if id, ok := arg.(*ast.Ident); ok {
		if obj := taggroupObjectOf(p.TypesInfo, id); obj != nil {
			_, bound := scopeBoundObjs[obj]
			return bound
		}
	}
	return false
}

// collectTypedScopeBoundObjects returns the set of types.Object bound — anywhere
// in file via a single `:=` / `=` / `var` — to a CallExpr resolving to one of
// the typed-scope constructors in taggroupTypedScopeCtors. This closes the
// scope-var-indirection bypass (F2): `scope := Typed(...); for _, g := range
// KnownNonDefaultTags() { Run(t, scope, ...) }`, where at the Run call site the
// 2nd arg is a plain *ast.Ident (the scope var) rather than a direct constructor
// CallExpr. Scope is file-level — EachInSubtree walks the WHOLE file AST
// (including function bodies, which is where the realistic copy-template binding
// lives), bounded to this one file — and single-binding; both constraints mirror
// collectKnownTagsBoundObjects (BS-4) exactly — same narrow accepted sub-gap for
// multi-RHS positional binding and cross-file/cross-func escape.
func collectTypedScopeBoundObjects(p *Pass, file *ast.File) map[types.Object]struct{} {
	out := make(map[types.Object]struct{})
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return
		}
		if !exprIsTypedScopeCtorCall(p, as.Rhs[0]) {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			taggroupBindIdent(p.TypesInfo, id, out)
		}
	})
	EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		if len(vs.Names) != 1 || len(vs.Values) != 1 {
			return
		}
		if !exprIsTypedScopeCtorCall(p, vs.Values[0]) {
			return
		}
		taggroupBindIdent(p.TypesInfo, vs.Names[0], out)
	})
	return out
}

// exprIsTypedScopeCtorCall reports whether expr is a CallExpr whose callee
// resolves via *types.Info to a member of taggroupTypedScopeCtors in the
// archtest façade package.
func exprIsTypedScopeCtorCall(p *Pass, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || pkgPath != taggroupLoopArchtestPkg {
		return false
	}
	_, isTyped := taggroupTypedScopeCtors[name]
	return isTyped
}

// isLiveTaggroupTarget restricts the live scan to tools/archtest/*_test.go
// direct children (excluding fixture sub-package files).
//
// Scope gap intentional: tools/archtest/internal/<subpkg>/*_test.go
// (e.g. internal/scanner/, internal/typeseval/) are NOT scanned. These
// sub-packages test internal symbols and do not call archtest.Run with a
// typed scope directly, so the loop-amortization invariant is not at risk there.
// If a future internal _test.go adds direct archtest.Run(t, Typed(...), ...)
// usage with tagGroup loops, scope extension is needed — re-evaluate at that point.
// See ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
// §威胁矩阵 for the accepted scope rationale.
func isLiveTaggroupTarget(rel string) bool {
	if !strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.Contains(rel, taggroupLoopFixtureDirSeg) {
		return false
	}
	// Must be tools/archtest/<file>_test.go exactly (no further nesting).
	const prefix = "tools/archtest/"
	if !strings.HasPrefix(rel, prefix) {
		return false
	}
	rest := strings.TrimPrefix(rel, prefix)
	return !strings.Contains(rest, "/")
}

// basenameWithoutExt returns the filename portion of rel with the .go suffix
// stripped, e.g. "tools/.../red_foo.go" -> "red_foo".
func basenameWithoutExt(rel string) string {
	i := strings.LastIndex(rel, "/")
	base := rel
	if i >= 0 {
		base = rel[i+1:]
	}
	return strings.TrimSuffix(base, ".go")
}
