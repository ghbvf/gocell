// INVARIANT: TYPESUTIL-IMPLEMENTS-FUNNEL-01
//
// TYPESUTIL-IMPLEMENTS-FUNNEL-01: every reference to the stdlib function
// go/types.Implements outside tools/typesutil/implements_interface.go is a
// funnel bypass. The sanctioned wrappers typesutil.ImplementsInterface
// (value-or-pointer) and typesutil.ImplementsInterfaceExact (value-only)
// are the only API the rest of the repo may use; re-inlining a raw
// types.Implements check (the "Nth re-inline" recurrence the R2-P2 PR-a
// consolidation removed) is structurally rejected here.
//
// AI-rebust: Hard (charter §"typed function call as Hard funnel for
// unbounded operations" 范本, isomorphic to PANIC-REGISTERED-01's
// type-resolution kernel). The callee/reference is resolved via
// *types.Info.Uses[ident] → *types.Func → Pkg().Path()=="go/types" &&
// Name()=="Implements", so a same-name local func, an import alias, or a
// dot-import cannot disguise the reference. Hard property = form
// uniqueness + archtest fail-on-deviation: there is no "looks like a
// go/types.Implements ref but isn't" gray zone.
//
// Funnel 双向锁 (charter §"Funnel 双向锁评级", 两栏强制):
//   - 下游 Hard: any file other than the funnel file referencing
//     go/types.Implements turns CI red. The reference identity is
//     type-resolved (no comment-anchor escape, no name convention, no
//     whitelist map).
//   - 上游: go/types.Implements is a stdlib symbol — Go's type system
//     cannot seal it (any package may import "go/types" and call it), the
//     same shape as panic(any). Honest caveat: the upstream guarantee is
//     archtest-bound (form-uniqueness + fail-on-deviation), NOT
//     compile-time. For stdlib symbols that Go's type system cannot seal,
//     archtest-bound form-uniqueness + fail-on-deviation IS the Hard
//     ceiling (not Medium) — this follows the PANIC-REGISTERED-01
//     precedent the charter blesses as a "typed function call as Hard
//     funnel for unbounded operations" 范本, which is why no backlog
//     Hard-upgrade item is registered. This is exactly the Hard 范本 the
//     charter blesses for this rule shape; because the info.Uses sweep
//     below covers EVERY reference form (call / dot-import / alias /
//     func-value), there is no bypass shape left as a gray zone.
//     Conclusion: closed Hard funnel — no backlog upgrade item is
//     registered (R2-P2 PR-b closes the cap-02 row in the same PR).
//
// Blind spot inventory (each item has a reverse self-check fixture or an
// honest scope declaration — charter §"工具选定后强制盲区自检"):
//   - call form types.Implements(...) — selector_call_red fixture.
//   - dot-import bare Implements(...) (import . "go/types") —
//     dot_import_red fixture.
//   - import-alias gt.Implements(...) (import gt "go/types") —
//     aliased_import_red fixture.
//   - func-value form `f := types.Implements` (reference NOT in
//     CallExpr.Fun position) — func_value_red fixture. This is the form a
//     CallExpr-only walk would miss; the info.Uses sweep makes it a
//     guarded case, not a blind spot.
//   - package-level `var fn = types.Implements` is subsumed by the
//     func-value coverage — info.Uses produces the same *types.Func entry
//     whether the reference is a local `:=` or a package-level `var`;
//     `func_value_red` exercises the equivalent Uses entry. Honest scope
//     declaration, no separate fixture needed.
//   - *_test.go files — IN SCOPE. TestTypesutilImplementsFunnel01 uses
//     RunTypedProduction with Tests:true so test-variant packages (every
//     *_test.go) are scanned. This is the critical surface, not a blind
//     spot: the 6 callsites R2-P2 PR-a consolidated all lived in
//     tools/archtest/*_test.go, so a test-file-excluding scope would make
//     the rule toothless. Guarded structurally by the sawTestFile
//     assertion in the test.
//   - files behind non-default build tags (//go:build integration / e2e
//     / …) — accepted boundary, NOT a meaningful gap for this symbol
//     class. go/types.Implements is the Go type-checker API; it appears
//     only in default-build static-analysis / tooling code (the 6 real
//     callsites + the funnel are all default-build). Runtime code behind
//     build tags does not import go/types to call Implements. The scan
//     deliberately does NOT fan out over KnownNonDefaultTags() (unlike
//     PANIC-REGISTERED-01, where panic() genuinely occurs in tagged
//     code) — that fan-out would add N redundant whole-module type loads
//     for ~zero coverage. Honest scope declaration.
//   - testdata/ RED fixtures (this rule's own intentional-violation
//     fixtures) — NOT covered by the implementsFunnelFileRel allowlist;
//     they are excluded from the production scan because
//     RunTypedProduction loads "./..." which Go excludes testdata/ from
//     by convention, and the fixture reverse self-check loads them via
//     explicit non-recursive RunTyped patterns. Honest scope
//     declaration: the only allowlist is the single funnel file; the
//     fixtures' exemption is path-scope, not allowlist.
//   - generated/ codegen output — excluded by RunTypedProduction (the
//     ProductionResolver generated/ funnel); codegen templates do not emit
//     go/types.Implements, so there is no enforcement gap. Honest scope
//     declaration.
//   - string / go:generate textual mentions — not a Go type reference;
//     out of scope by construction (info.Uses only carries resolved
//     identifier objects). Honest scope declaration.
//
// ref: tools/typesutil/implements_interface.go — the single funnel
//
//	definition (ImplementsInterface / ImplementsInterfaceExact).
//
// ref: tools/archtest/panic_invariants_test.go — companion Hard pattern
//
//	(PANIC-REGISTERED-01, info.Uses → *types.Func identity).
//
// ref: docs/plans/202605162000-037r2-wave4-advance-round2.md §R2-P2 PR-b.
package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleTypesutilImplementsFunnel01 = "TYPESUTIL-IMPLEMENTS-FUNNEL-01"
	goTypesPkgPath                  = "go/types"
	goTypesImplementsFunc           = "Implements"
	// implementsFunnelFileRel is the single module-relative file allowed to
	// reference go/types.Implements directly (the consolidated funnel from
	// R2-P2 PR-a). Every other reference is a bypass.
	implementsFunnelFileRel = "tools/typesutil/implements_interface.go"
)

// collectImplementsFunnelViolations sweeps p.TypesInfo.Uses across every
// file in the Pass and reports each *ast.Ident that resolves to the
// go/types.Implements *types.Func, unless the file is the sanctioned
// funnel file. Sweeping Uses (rather than only CallExpr.Fun) is what makes
// the rule Hard: call form, dot-import bare form, import-alias form, and
// func-value form (`f := types.Implements`) all produce a Uses entry whose
// object is the same *types.Func, so no reference shape escapes.
//
// The funnel-file path comparison is the ONLY allowlist; it never matches
// a fixture path, so the same detector is reused unchanged by both the
// production test and the fixture reverse self-test.
func collectImplementsFunnelViolations(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var diags []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		if rel == implementsFunnelFileRel {
			continue // the one sanctioned site
		}
		EachInSubtree[ast.Ident](f, func(id *ast.Ident) {
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil {
				return
			}
			if fn.Pkg().Path() != goTypesPkgPath || fn.Name() != goTypesImplementsFunc {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(id.Pos()).Line,
				Message: "raw go/types.Implements reference outside " +
					implementsFunnelFileRel +
					"; use typesutil.ImplementsInterface (value-or-pointer) or" +
					" typesutil.ImplementsInterfaceExact (value-only)" +
					" — see tools/typesutil/implements_interface.go",
			})
		})
	}
	return diags
}

// TestTypesutilImplementsFunnel01 enforces the rule module-wide. After
// R2-P2 PR-a (#540) consolidated all 6 call sites into the funnel file,
// this test must pass with zero violations.
//
// CRITICAL — Tests:true is mandatory, not optional. The 6 consolidated
// callsites all live in tools/archtest/*_test.go (archtest rules ARE test
// files). With Tests:false, RunTypedProduction never loads any *_test.go,
// so re-inlining a raw go/types.Implements in any _test.go would NOT turn
// this rule red — the funnel would be toothless exactly where the original
// duplication lived. Tests:true loads test-variant packages so every
// *_test.go is in scope; sawTestFile below is a structural regression
// guard that fails loud if a future edit silently flips the scope back to
// Tests:false (the gap would otherwise be invisible — the scan would stay
// vacuously green).
//
// A single RunTypedProduction (default tags, Tests:true) replaces the
// earlier per-KnownNonDefaultTags loop: go/types.Implements is a
// compile-time go/types type-checker primitive that only appears in
// default-build tooling code (the 6 real callsites + the funnel are all
// default-build), so a per-build-tag fan-out adds ~zero coverage while
// driving N redundant whole-module type loads — see the build-tag bullet
// in the blind-spot inventory for the honest scope declaration. (This
// differs from PANIC-REGISTERED-01, which loops tags because panic()
// genuinely occurs in build-tag-gated runtime code.)
func TestTypesutilImplementsFunnel01(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	sawTestFile := false

	_ = RunTypedProduction(t, TypedOpts{Tests: true}, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			if strings.HasSuffix(p.Rel(f), "_test.go") {
				sawTestFile = true
			}
		}
		violations = append(violations, collectImplementsFunnelViolations(p)...)
		return nil
	})

	require.True(t, sawTestFile,
		"%s scope regression: Tests:true must surface *_test.go — the 6 "+
			"consolidated callsites are archtest test files; a Tests:false "+
			"scope would make the funnel toothless. See function godoc.",
		ruleTypesutilImplementsFunnel01)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleTypesutilImplementsFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.Rel, v.Line, v.Message)
		}
	}
	assert.Empty(t, violations,
		"%s: go/types.Implements must only be referenced from %s; "+
			"use typesutil.ImplementsInterface / ImplementsInterfaceExact elsewhere.",
		ruleTypesutilImplementsFunnel01, implementsFunnelFileRel)
}

// TestTypesutilImplementsFunnel01_Fixtures is the reverse self-check
// mandated by charter §"工具选定后强制盲区自检": it proves the detector
// actually fires for every reference form listed in the blind-spot
// inventory. RED fixtures must report a violation on the exact ident line;
// the GREEN fixture (routes through the funnel) must report zero.
//
// Each fixture pattern is non-recursive (RunTyped, not RunTypedDir) so
// RunTyped yields exactly the fixture package as a Pass (deps are loaded
// for type info but not yielded as Passes), and the funnel-file allowlist
// never matches a fixture path. RunTyped is used here (rather than
// RunTypedDir) because approved_wrapper_green imports the main-module
// package github.com/ghbvf/gocell/tools/typesutil; an isolated fixture
// module (RunTypedDir) would require a replace directive. RunTyped with
// explicit non-recursive patterns is the correct entry here, matching the
// panic_registered_fixtures precedent.
func TestTypesutilImplementsFunnel01_Fixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dir       string
		wantLines []int // nil = GREEN (0 violations)
	}{
		{"selector_call_red", []int{9}},
		{"dot_import_red", []int{9}},
		{"aliased_import_red", []int{10}},
		{"func_value_red", []int{13}},
		{"approved_wrapper_green", nil},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()

			pattern := "./tools/archtest/testdata/typesutil_implements_fixtures/" + tc.dir

			var gotLines []int
			scanned := false
			_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
				if len(p.Files) > 0 {
					scanned = true
				}
				for _, d := range collectImplementsFunnelViolations(p) {
					gotLines = append(gotLines, d.Line)
				}
				return nil
			})
			require.True(t, scanned, "fixture %s: no package loaded (path renamed?)", tc.dir)
			sort.Ints(gotLines)

			wantLines := append([]int(nil), tc.wantLines...)
			sort.Ints(wantLines)

			assert.Equal(t, wantLines, gotLines,
				"fixture %s: violation lines mismatch", tc.dir)
		})
	}
}
