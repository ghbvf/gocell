package archtest

// invariants:
//   - INVARIANT: TAGGROUP-LOOP-FORBIDS-RUNTYPED-01

import (
	"fmt"
	"go/ast"
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
// Hard form-uniqueness (callee, body) — pairs with ai-collab.md §"Hard 范本"
// 第 2 条 (panic register Approved):
//
//   (i) RangeStmt.X must be *ast.CallExpr whose Fun resolves via *types.Info
//       to typeseval.KnownNonDefaultTags (covers archtest re-export and direct
//       typeseval import; both lower to the same *types.Func identity).
//   (ii) RangeStmt.Body, walked recursively, must contain *ast.CallExpr whose
//        Fun resolves via *types.Info to tools/archtest.RunTyped.
//
// Both conditions AND → violation. Either side alone is fine: a tagGroup
// loop that only inspects the slice (no RunTyped) is OK; a RunTyped call
// outside a tagGroup loop is OK.
//
// Compliant idioms (see fixture green_*):
//   - single RunTyped(t, TypedOpts{Tags: ProductionFlatTags()}, ...)
//   - two RunTyped calls: tags=nil + tags=ProductionFlatTags() with shared
//     seen-map dedup (defensive: covers reverse build directives //go:build
//     !X which are silently excluded from a -tags=...,X,... union load)
//
// AI-rebust grade: Hard (typed-function-call funnel with (callee, body)
// double-factor form-uniqueness). Same shape and termination criteria as
// PANIC-REGISTERED-01 — there is no "looks-like-KnownNonDefaultTags but
// isn't" gray zone because the identity check goes through *types.Info.
//
// Scope: tools/archtest/*_test.go (direct children only — fixtures under
// tools/archtest/internal/taggrouploopfixtures/ are excluded from the live
// scan and exercised by Test_TaggroupLoopFixturePrecisionGate below).
//
// Blind-spot self-checks (ai-collab.md §"工具选定后强制盲区自检"):
//
//   1. Façade vs internal callee: a fixture must exercise both
//      archtest.KnownNonDefaultTags (façade re-export in resolve.go) and
//      typeseval.KnownNonDefaultTags (direct internal import). Both point at
//      the same *types.Func object — covered by red_panic_invariants_style.go
//      and red_typeseval_qualified.go.
//   2. Nested-closure body: RunTyped invoked inside an IIFE within the loop
//      body must still be caught (subtree walk, not direct-child walk).
//      Covered by red_nested_closure.go.
//   3. Patterns variance: the loop-amortization invariant is independent of
//      the patterns arg shape; subpath patterns like ./cells/... must also
//      be caught when wrapped in a tagGroup loop. Covered by
//      red_subpath_runtyped.go.
//
// Reverse fixture-precision self-check enforces that GREEN fixtures (single
// RunTyped(ProductionFlatTags()), two-load nil+ProductionFlatTags) do NOT
// trip the rule.

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
		"red_panic_invariants_style",
		"red_typeseval_qualified",
		"red_nested_closure",
		"red_subpath_runtyped",
	}
	for _, name := range expectedRed {
		if hits[name] == 0 {
			t.Errorf("RED fixture %s.go: expected to be caught, but the rule produced no diagnostic for it", name)
		}
	}

	disallowedGreen := []string{
		"green_single_load_production_flat",
		"green_two_loads_nil_and_flat",
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
	EachInSubtree[ast.RangeStmt](file, func(rs *ast.RangeStmt) {
		if !rangeExprCallsKnownNonDefaultTags(p, rs.X) {
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
					"replace with single RunTyped(Tags: archtest.ProductionFlatTags()) "+
					"or two-load nil+ProductionFlatTags pattern (see ADR 202605190000)",
				taggroupLoopKnownTagsName, taggroupLoopRunTypedName),
		})
	})
	return out
}

// rangeExprCallsKnownNonDefaultTags reports whether expr is a CallExpr whose
// Fun resolves to KnownNonDefaultTags in either archtest (façade re-export)
// or typeseval (internal).
func rangeExprCallsKnownNonDefaultTags(p *Pass, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok {
		return false
	}
	if name != taggroupLoopKnownTagsName {
		return false
	}
	return pkgPath == taggroupLoopArchtestPkg || pkgPath == taggroupLoopTypesevalPkg
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
