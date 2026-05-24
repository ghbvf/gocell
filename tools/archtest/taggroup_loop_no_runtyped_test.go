package archtest

// INVARIANT: TAGGROUP-LOOP-FORBIDS-RUNTYPED-01

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

// TAGGROUP-LOOP-FORBIDS-RUNTYPED-01 — backstory and form-uniqueness
//
// Backstory: panic_invariants_test.go::TestPanicRegistered once wrote the
// pattern
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
//   (i) RangeStmt.X resolves via *types.Info to typeseval.KnownNonDefaultTags,
//       either as a direct *ast.CallExpr (covers archtest re-export, direct
//       typeseval import, and aliased import — all lower to the same
//       *types.Func identity) OR as an *ast.Ident bound — within the same
//       FuncDecl/FuncLit — by `:=` / `=` / `var` from such a CallExpr (the
//       var-indirection form; see blind-spot closure BS-4 below).
//   (ii) RangeStmt.Body, walked recursively, must contain *ast.CallExpr whose
//        Fun resolves via *types.Info to tools/archtest.RunTyped.
//
// Both conditions AND → violation. Either side alone is fine: a tagGroup
// loop that only inspects the slice (no RunTyped) is OK — this is a real
// legitimate idiom: pass_test.go binds KnownNonDefaultTags() to a var for
// façade↔oracle parity assertion (no range, no RunTyped); a RunTyped call
// outside a tagGroup loop is OK.
//
// Compliant idioms (see fixture green_*):
//   - single RunTyped(t, TypedOpts{Tags: FlatNonDefaultTags()}, ...)
//   - two RunTyped calls: tags=nil + tags=FlatNonDefaultTags() with shared
//     seen-map dedup (defensive: covers reverse build directives //go:build
//     !X which are silently excluded from a -tags=...,X,... union load)
//
// AI-robust grade: Hard (typed-function-call funnel with (callee, body)
// double-factor form-uniqueness). Same shape and termination criteria as
// PANIC-REGISTERED-01 — there is no "looks-like-KnownNonDefaultTags but
// isn't" gray zone because the identity check goes through *types.Info.
//
// Scope: tools/archtest/*_test.go (direct children only — fixtures under
// tools/archtest/internal/taggrouploopfixtures/ are excluded from the live
// scan and exercised by Test_TaggroupLoopFixturePrecisionGate below).
//
// Blind-spot self-checks (ai-robust.md §"工具选定后强制盲区自检").
//
// rangeExprCallsKnownNonDefaultTags / bodyContainsRunTyped resolve callee
// identity through *types.Info — staticcheck SA4000 style (direct AST +
// types.Info form-matching, no value-flow chasing). The forms below are
// outside that resolver's declared range; each is either CLOSED with a RED
// fixture or an ACCEPTED blind spot with rationale.
//
//   BS-1 Façade vs internal callee (CLOSED): fixtures exercise both
//      archtest.KnownNonDefaultTags (façade re-export in resolve.go) and
//      typeseval.KnownNonDefaultTags (direct internal import). Both point at
//      the same *types.Func object — covered by red_panic_invariants_style.go
//      and red_typeseval_qualified.go. Alias import form (kt "…/typeseval")
//      also resolves to the same *types.Func identity — red_aliased_import.go.
//   BS-2 Nested-closure body (CLOSED): RunTyped invoked inside an IIFE within
//      the loop body must still be caught (subtree walk, not direct-child
//      walk). Covered by red_nested_closure.go.
//   BS-3 Patterns variance (CLOSED): the loop-amortization invariant is
//      independent of the patterns arg shape; subpath patterns like
//      ./cells/... must also be caught when wrapped in a tagGroup loop.
//      Covered by red_subpath_runtyped.go.
//   BS-4 RangeStmt.X var-indirection (CLOSED): `tags := KnownNonDefaultTags();
//      for _, g := range tags { RunTyped(...) }`. condition (i) now also
//      matches an *ast.Ident bound from KnownNonDefaultTags() within the same
//      FuncDecl/FuncLit (collectKnownTagsBoundObjects). Precision is proven
//      both directions: red_var_bound_range.go (bind→range→RunTyped → caught)
//      and green_var_bound_parity.go (bind→len-assert, no range/RunTyped, the
//      pass_test.go façade-parity idiom → NOT caught, no false positive).
//      Narrow accepted sub-gap: only single-binding (`tags := f()` /
//      `var tags = f()`) is recognized; multi-RHS positional binding
//      (`a, tags := x, f()`) is not — it is not a copy template a fresh
//      instance reproduces, and single-element indexed access keeps the
//      collector compliant with SCANNER-FRAMEWORK-USAGE-01 (no for-range +
//      type-assert over []ast.Expr).
//   BS-5 Helper-wrapped RunTyped in loop body (ACCEPTED blind spot): a loop
//      body that calls a package-local helper which itself calls RunTyped is
//      not caught — bodyContainsRunTyped resolves the direct callee only and
//      does not perform intra-package call-graph reachability. Rationale:
//      (a) closing it requires one-level (or recursive) call-graph analysis,
//      disproportionate to a rule whose purpose is anti-copy of one specific
//      historical template — the template a fresh-instance Claude reproduces
//      is the *direct* RunTyped-in-body shape (the now-removed
//      panic_invariants loop / the red fixtures), not a helper-wrapped one;
//      (b) a precise reverse self-check is infeasible — `func foo(){ RunTyped
//      (...) }` local helpers are a pervasive legitimate idiom across the
//      archtest suite, so any blanket "no local helper reaches RunTyped"
//      assertion would false-positive (the same accepted trade-off
//      staticcheck SA4000 documents for fn()==fn() side-effect blindness).
//      Upgrade path: one-level call-graph analysis; trigger =
//      first real event of a helper-wrapped tagGroup loop reaching CI.
//
// Reverse fixture-precision self-check (Test_TaggroupLoopFixturePrecisionGate)
// enforces that GREEN fixtures (single RunTyped(FlatNonDefaultTags()),
// two-load nil+FlatNonDefaultTags, and the BS-4 parity binding) do NOT trip
// the rule.

const (
	taggroupLoopRuleID         = "TAGGROUP-LOOP-FORBIDS-RUNTYPED-01"
	taggroupLoopKnownTagsName  = "KnownNonDefaultTags"
	taggroupLoopRunTypedName   = "RunTyped"
	taggroupLoopArchtestPkg    = "github.com/ghbvf/gocell/tools/archtest"
	taggroupLoopTypesevalPkg   = "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	taggroupLoopFixtureDirSeg  = "tools/archtest/internal/taggrouploopfixtures/"
	taggroupLoopPatternProd    = "./tools/archtest/..."
	taggroupLoopPatternFixture = "./tools/archtest/internal/taggrouploopfixtures/..."
)

// TestTagGroupLoopForbidsRunTyped enforces TAGGROUP-LOOP-FORBIDS-RUNTYPED-01
// against production archtest *_test.go files. Fixture files under
// tools/archtest/internal/taggrouploopfixtures/ are excluded — the live scan
// is _test.go-only by RunTyped's package selection, and fixture .go files
// live in a separate package outside that selection.
//
// F3 cacheKey note: this scan's cacheKey is (modRoot, Tests=true, nil,
// "./tools/archtest/...") — deliberately NOT the (modRoot, false, nil,
// "./...") key TestMain warms. The amortization in ADR 202605190000 does not
// cover this rule's own scan; it pays one cold packages.Load. That is
// accepted (a single test, scope is bounded to one package). If it lands
// borderline on the 20s slowgate post-merge, that is expected behavior, not
// a regression — it is tracked under the slowgate cleanup
// (ARCHTEST-SLOWGATE-ALLOWLIST-CLEANUP-01, subprocess/cache-miss triage).
func TestTagGroupLoopForbidsRunTyped(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{Tests: true},
		[]string{taggroupLoopPatternProd},
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

	Report(t, taggroupLoopRuleID, diags)
}

// Test_TaggroupLoopFixturePrecisionGate verifies the rule's accuracy against
// the curated red/green fixture set: every red_*.go must trigger at least
// one diagnostic; no green_*.go may trigger any.
//
// Note: TypedOpts{} (Tests=false) here differs intentionally from the
// live scan's TypedOpts{Tests: true}. Fixture files use anonymous
// `func _(...)` declarations (not Test* entries), so test-variant mode
// is unnecessary; the underlying *types.Info pipeline is the same.
func Test_TaggroupLoopFixturePrecisionGate(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{}, []string{taggroupLoopPatternFixture},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				out = append(out, scanFileForTaggroupViolations(p, file, rel)...)
			}
			return out
		})

	hits := make(map[string]int)
	for _, d := range diags {
		base := basenameWithoutExt(d.Rel)
		hits[base]++
	}

	expectedRed := []string{
		"red_aliased_import",
		"red_nested_closure",
		"red_panic_invariants_style",
		"red_subpath_runtyped",
		"red_typeseval_qualified",
		"red_var_bound_range",
	}
	for _, name := range expectedRed {
		if hits[name] == 0 {
			t.Errorf("RED fixture %s.go: expected to be caught, but the rule produced no diagnostic for it", name)
		}
	}

	disallowedGreen := []string{
		"green_single_load_production_flat",
		"green_two_loads_nil_and_flat",
		"green_var_bound_parity",
	}
	for _, name := range disallowedGreen {
		if hits[name] > 0 {
			t.Errorf("GREEN fixture %s.go: unexpectedly caught by rule (%d diagnostic(s)) — false positive", name, hits[name])
		}
	}
}

// scanFileForTaggroupViolations walks file looking for RangeStmt nodes whose
// range expression calls KnownNonDefaultTags AND whose body subtree contains
// a RunTyped call. Uses [EachInSubtree] (the only allowed walk path per
// SCANNER-FRAMEWORK-USAGE-01) for both the outer RangeStmt enumeration and
// the inner CallExpr search.
func scanFileForTaggroupViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	boundObjs := collectKnownTagsBoundObjects(p, file)
	EachInSubtree[ast.RangeStmt](file, func(rs *ast.RangeStmt) {
		if !rangeExprCallsKnownNonDefaultTags(p, rs.X, boundObjs) {
			return
		}
		bodyHit, hitLine := bodyContainsRunTyped(p, rs.Body)
		if !bodyHit {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: hitLine,
			Message: fmt.Sprintf(
				"for-range over %s with %s inside loop body — "+
					"replace with single RunTyped(Tags: archtest.FlatNonDefaultTags()) "+
					"or two-load nil+FlatNonDefaultTags pattern "+
					"(see ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md;"+
					" FlatNonDefaultTags defined in tools/archtest/resolve.go)",
				taggroupLoopKnownTagsName, taggroupLoopRunTypedName,
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
	bindIdent := func(id *ast.Ident) {
		if id == nil || id.Name == "_" {
			return
		}
		if obj := taggroupObjectOf(p.TypesInfo, id); obj != nil {
			out[obj] = struct{}{}
		}
	}
	rhsResolvesToKnownTags := func(expr ast.Expr) bool {
		call, ok := expr.(*ast.CallExpr)
		return ok && callResolvesToKnownNonDefaultTags(p, call)
	}
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		if len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return
		}
		if !rhsResolvesToKnownTags(as.Rhs[0]) {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			bindIdent(id)
		}
	})
	EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		if len(vs.Names) != 1 || len(vs.Values) != 1 {
			return
		}
		if rhsResolvesToKnownTags(vs.Values[0]) {
			bindIdent(vs.Names[0])
		}
	})
	return out
}

// taggroupObjectOf returns the types.Object an identifier refers to, checking
// Defs (declaration site, e.g. LHS of `:=` / `var`) then Uses (reference
// site, e.g. range expression or LHS of plain `=`).
func taggroupObjectOf(info *types.Info, id *ast.Ident) types.Object {
	if obj := info.Defs[id]; obj != nil {
		return obj
	}
	return info.Uses[id]
}

// bodyContainsRunTyped walks body recursively and reports whether any
// descendant CallExpr's Fun resolves to archtest.RunTyped. Returns the line
// number of the first matching CallExpr (first in preorder). Walks the
// entire subtree (no early-return) — RangeStmt body sizes in archtest tests
// are small, so the constant-factor cost is irrelevant; the API choice keeps
// the rule honest about scanner usage.
func bodyContainsRunTyped(p *Pass, body *ast.BlockStmt) (bool, int) {
	var found bool
	var line int
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if name != taggroupLoopRunTypedName {
			return
		}
		if pkgPath != taggroupLoopArchtestPkg {
			return
		}
		found = true
		line = p.Fset.Position(call.Pos()).Line
	})
	return found, line
}

// isLiveTaggroupTarget restricts the live scan to tools/archtest/*_test.go
// direct children (excluding fixture sub-package files).
//
// Scope gap intentional: tools/archtest/internal/<subpkg>/*_test.go
// (e.g. internal/scanner/, internal/typeseval/) are NOT scanned. These
// sub-packages test internal symbols and do not call archtest.RunTyped
// directly, so the loop-amortization invariant is not at risk there.
// If a future internal _test.go adds direct archtest.RunTyped usage with
// tagGroup loops, scope extension is needed — re-evaluate at that point.
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
