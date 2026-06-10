//go:build archtest

// INVARIANT: RUNSCOPE-CONSTRUCTOR-FUNNEL-01
//
// # RUNSCOPE-CONSTRUCTOR-FUNNEL-01
//
// Invariant: a composite literal of one of the five sealed RunScope structs —
// astRunScope / typedRunScope / productionRunScope / dirRunScope /
// fixtureRunScope — may appear ONLY inside its sanctioned constructor body
// (AST / Typed / Production / StandaloneModule / Fixture). Anywhere else in
// package archtest it is a violation.
//
// # Why this exists (the in-package leg of the RunScope seal)
//
// [RunScope] is sealed via the unexported isRunScope() method, so no type
// OUTSIDE package archtest can implement it — that is the Hard DOWNSTREAM /
// package-external guarantee (an external Cell repo, or any non-archtest
// package, cannot forge a production/fixture scope or inject a build tag into a
// fixture scope).
//
// But every GoCell archtest rule lives IN package archtest (*_test.go direct
// children). Being same-package, a business rule CAN construct the unexported
// scope structs directly:
//
//	Run(t, typedRunScope{opts: TypedOpts{Tags: []string{"archtest_fixture"}}, …}, rule)
//
// — sidestepping Fixture's tag injection (the only sanctioned fixture loader).
// PASS-FUNNEL-FIXTURE-TAG-01 does NOT catch this: it fires on a CallExpr whose
// callee resolves to a loader in fixtureTagLoaderSet, and a bare
// typedRunScope{…} composite literal is not a CallExpr to Typed/Production/etc.
// This rule closes exactly that struct-literal hole — it is load-bearing, not
// merely a grading note.
//
// # AI-robust: Medium (in-package), Hard downstream (package-external)
//
// Downstream / package-external is Hard: sealed isRunScope() makes forging a
// RunScope outside package archtest a compile error.
//
// In-package is Medium: Go package visibility cannot express "no code INSIDE
// package archtest may construct typedRunScope". This archtest is the
// type-aware CI backstop. It flags two construction forms — composite literals
// (`typedRunScope{…}`, including `&…` and type-alias literals) and zero-value
// var declarations (`var s typedRunScope`, whose fields are then
// same-package-assignable) — resolving each via *types.Info (TypeOf +
// types.Unalias) to a *types.Named whose object is one of the five scope structs
// in package archtest, then asserts the enclosing top-level FuncDecl (via
// ResolveEnclosingFunc) is that struct's OWN sanctioned constructor (exact
// pairing — a typedRunScope built inside Fixture is still a violation). Name
// matching alone is insufficient (same-package construction is a bare Ident, not
// a qualified selector), so the rule is types-bound, not string-bound — an
// import alias / rename / type alias cannot bypass it.
//
// The permanent Go-language ceiling (a same-package *_test.go CAN construct the
// unexported struct, Go cannot forbid it at compile time) is the same shape
// documented at #851 (SPAN-SETATTR-HOLDER-SEAL) / #893 (HEALTHZ-HOLDER-SEAL) /
// #1282 (outbox principal-write) / #1424 (slog handwritten seal). The true-Hard
// upgrade (move the scope structs + Run dispatch + Pass into
// tools/archtest/internal/driver and re-export via type aliases) is tracked as
// a deliberate won't-do at gh #1485.
//
// # Blind spots (per ai-robust.md Medium evidence requirement)
//
//   - New named type DERIVED from a scope struct (`type x typedRunScope; x{}`):
//     NOT detected — but harmless, because a defined type does NOT inherit the
//     method set of its underlying struct, so x does not implement isRunScope()
//     and cannot reach Run. (A type ALIAS `type x = typedRunScope` IS detected:
//     under gotypesalias=1 TypeOf(x{}) is a *types.Alias, and types.Unalias in
//     runScopeNamedType resolves it to the identical named typedRunScope.)
//   - Pointer literal `&typedRunScope{}`: IS detected — the inner CompositeLit
//     node is typed `typedRunScope`, so TypeOf(comp) resolves it. (Note: a
//     VALUE-receiver isRunScope() IS promoted to the `*typedRunScope` method
//     set, so `*typedRunScope` DOES implement RunScope and
//     `Run(t, &typedRunScope{}, rule)` compiles — it is NOT a compile error; it
//     fails at runtime in Run's default branch because the dispatch switch has no
//     `case *typedRunScope`. Either way the inner literal is flagged, so this is
//     not a bypass.)
//   - Reflect / runtime construction: outside Go static AST + types.Info scope,
//     same accepted boundary as the entire archtest framework.
//   - A scope struct returned from a non-constructor helper that is itself
//     wrapped by a constructor: not applicable — the five structs have no
//     factory other than their five constructors, and adding one is exactly the
//     change this rule's per-member fixture lock would force into the pairing.
//
// # Reverse self-check
//
//   - TestRunScopeConstructorFunnel01_FixtureCoverage loads the archtest_fixture
//     in-package RED fixture (runscope_ctor_redfixture.go) and asserts each of
//     the five struct names is flagged (per-member trip-wire) AND that the total
//     count equals exactly SEVEN — five direct composite literals + one
//     type-alias literal (locks types.Unalias) + one zero-value var declaration
//     (locks the Form-2 var-decl scan). Both new precision legs are thus
//     load-bearing: dropping Unalias or the var-decl scan drops the count below
//     seven, and a constructor wrongly flagged would push it above seven.
//   - TestRunScopeConstructorFunnel01 (the live gate) asserts production package
//     archtest (no fixture tag) has ZERO violations — every scope-struct
//     construction in pass.go / fixture.go is inside its own constructor. This
//     is also the only reverse-check for the exact-pairing leg (F3): a mispaired
//     construction can structurally only live inside a sanctioned-named ctor
//     (anywhere else is already flagged by the not-in-a-ctor check), and a
//     fixture cannot redefine AST/Typed/Production/StandaloneModule/Fixture
//     without a name collision — so a mispairing surfaces as a live-gate red on
//     pass.go / fixture.go, not as an isolated fixture trip-wire.
package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const runScopeConstructorFunnelRuleID = "RUNSCOPE-CONSTRUCTOR-FUNNEL-01"

// runScopeStructToCtor is the single source pairing each sealed RunScope struct
// with the ONE sanctioned constructor allowed to build it. It is both the
// membership set (a name absent from the map is not a tracked scope struct) and
// the struct→constructor pairing (F3: a scope-struct construction inside a
// DIFFERENT scope constructor is a mispairing, still a violation). Adding a
// sixth RunScope struct MUST add it here (with its constructor) or the new
// struct is constructible anywhere in-package — the red fixture's per-member
// trip-wire forces this maintenance.
var runScopeStructToCtor = map[string]string{
	"astRunScope":        "AST",
	"typedRunScope":      "Typed",
	"productionRunScope": "Production",
	"dirRunScope":        "StandaloneModule",
	"fixtureRunScope":    "Fixture",
}

// scanRunScopeConstructorViolations walks file for constructions of the five
// sealed RunScope structs and reports each one not lexically inside its OWN
// sanctioned constructor body. Two construction forms are covered:
//
//	Form 1 — composite literal `typedRunScope{…}` (incl. `&typedRunScope{}` and
//	         type-alias `aliasOfTyped{…}`, both resolved via types.Unalias).
//	Form 2 — zero-value var declaration `var s typedRunScope` (no composite
//	         literal node, so Form 1's CompositeLit scan cannot see it; the
//	         fields are then same-package-assignable: `s.opts = …`).
func scanRunScopeConstructorViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var out []Diagnostic
	flag := func(name string, node ast.Node) {
		if scopeConstructionSanctioned(p, file, node, name) {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(node.Pos()).Line,
			Message: "construction of sealed RunScope " + name + " outside its " +
				"sanctioned constructor; build the scope via " +
				"archtest.{AST,Typed,Production,StandaloneModule,Fixture} and pass it " +
				"to Run(t, <RunScope>, rule) — RUNSCOPE-CONSTRUCTOR-FUNNEL-01",
		})
	}
	// Form 1: composite literals (alias + &-elided resolve via types.Unalias).
	EachInSubtree[ast.CompositeLit](file, func(comp *ast.CompositeLit) {
		if name, ok := runScopeNamedType(p.TypesInfo.TypeOf(comp)); ok {
			flag(name, comp)
		}
	})
	// Form 2: zero-value var declarations `var s typedRunScope`. A spec with no
	// explicit Type (`var s = typedRunScope{}`) carries its construction in a
	// composite literal already covered by Form 1, so only typed specs are
	// scanned here (no double counting).
	EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		if vs.Type == nil {
			return
		}
		if name, ok := runScopeNamedType(p.TypesInfo.TypeOf(vs.Type)); ok {
			flag(name, vs.Type)
		}
	})
	return out
}

// scopeConstructionSanctioned reports whether a construction of the scope struct
// structName at node sits inside that struct's OWN sanctioned constructor in
// package archtest. Pairing is exact (F3): a typedRunScope built inside
// Fixture's body is NOT sanctioned. archtestPkgPath
// ("github.com/ghbvf/gocell/tools/archtest") is the shared meta-archtest const
// declared in pass_funnel_test.go (same package archtest test binary).
func scopeConstructionSanctioned(p *Pass, file *ast.File, node ast.Node, structName string) bool {
	fn, found := ResolveEnclosingFunc(p.TypesInfo, file, node)
	if !found || fn.Pkg() == nil || fn.Pkg().Path() != archtestPkgPath {
		return false
	}
	wantCtor, ok := runScopeStructToCtor[structName]
	return ok && fn.Name() == wantCtor
}

// runScopeNamedType resolves t via *types.Info to a named struct in package
// archtest and returns its name when it is one of the five sealed RunScope
// structs. types.Unalias is mandatory: under gotypesalias=1 (the default since
// Go 1.23) the type of a type-alias literal `aliasOfTyped{}` is a *types.Alias,
// which a bare `.(*types.Named)` assertion would miss — letting an in-package
// alias literal bypass the funnel (F2).
func runScopeNamedType(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	if named.Obj().Pkg().Path() != archtestPkgPath {
		return "", false
	}
	name := named.Obj().Name()
	if _, ok := runScopeStructToCtor[name]; ok {
		return name, true
	}
	return "", false
}

// TestRunScopeConstructorFunnel01 is the live gate: production package archtest
// (loaded WITHOUT the archtest_fixture tag, so the RED fixture is invisible)
// must construct the five sealed RunScope structs ONLY inside their
// constructors. Tests:true includes business *_test.go rules — the actual
// in-package bypass surface — so a future rule that forges a scope struct
// directly reds here.
func TestRunScopeConstructorFunnel01(t *testing.T) {
	// Tags: archtest — the in-package rule files are //go:build archtest gated
	// (ARCHTEST-LEAF-BUILD-TAG-01); Tests:true alone no longer surfaces the
	// *_test.go bypass surface this gate must scan, so the load would go vacuous.
	diags := Run(t, Typed(TypedOpts{Tests: true, Tags: []string{"archtest"}}, []string{"./tools/archtest"}),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				out = append(out, scanRunScopeConstructorViolations(p, file, p.Rel(file))...)
			}
			return out
		})
	Report(t, runScopeConstructorFunnelRuleID, diags)
}

// TestRunScopeConstructorFunnel01_FixtureCoverage is the AI-robust reverse
// self-check: it loads package archtest WITH the archtest_fixture tag (so
// runscope_ctor_redfixture.go becomes visible) via the sanctioned Fixture
// loader, and asserts the detector flags each of the five sealed RunScope
// structs exactly once.
//
// Tests:false keeps the load to non-test .go (NOT a test-exclusion claim — it
// just means business *_test.go rules are out of this coverage load; the five
// sanctioned constructors live in pass.go/fixture.go, which are non-test, so
// they are present and must NOT be flagged). With Tests:true the same property
// holds — business *_test.go would add only ctor-mediated Run calls, never bare
// scope-struct literals — but Tests:false keeps this coverage load minimal.
//
// The constructions seen are: the five sanctioned constructors (NOT flagged) +
// the RED fixture constructions (flagged): five direct composite literals + one
// type-alias literal (Form 1, F2 — proves types.Unalias) + one zero-value var
// declaration (Form 2, F1 — proves the var-decl scan) = seven. The
// GREEN-negative fixture (runScopeConstructorGreenNegatives: TypedOpts /
// FixtureOpts / Diagnostic literals AND a non-scope var-decl, outside any ctor)
// MUST add zero — so the exact-count==7 lock is also a false-positive guard: a
// non-scope name mistakenly tracked, or a Form-2 scan that over-flags non-scope
// vars, would trip a GREEN line and push the count past seven.
func TestRunScopeConstructorFunnel01_FixtureCoverage(t *testing.T) {
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{"./tools/archtest"}),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				out = append(out, scanRunScopeConstructorViolations(p, file, p.Rel(file))...)
			}
			return out
		})

	perStruct := make(map[string]int, len(runScopeStructToCtor))
	for _, d := range diags {
		for name := range runScopeStructToCtor {
			if containsStructName(d.Message, name) {
				perStruct[name]++
			}
		}
	}
	missing := make([]string, 0)
	for name := range runScopeStructToCtor {
		if perStruct[name] == 0 {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s FixtureCoverage: struct %q produced 0 "+
			"diagnostics on the in-package RED fixture; either runscope_ctor_redfixture.go "+
			"dropped this struct's construction or the detector regressed for it "+
			"(per-member regression lock)", runScopeConstructorFunnelRuleID, name)
	}
	// Exact-count lock: seven RED constructions (5 direct literals + 1 alias
	// literal + 1 var-decl), and the five sanctioned constructors + the
	// GREEN-negative non-scope constructions must add zero. Drift (a constructor
	// wrongly flagged, a non-scope name wrongly tracked, types.Unalias or the
	// Form-2 var-decl scan dropped, or a new RED construction without updating
	// this count) fails here.
	const wantViolations = 7
	if got := len(diags); got != wantViolations {
		t.Errorf("%s FixtureCoverage: %d violations, want %d "+
			"(5 direct + 1 alias + 1 var-decl RED constructions trip once each; the "+
			"five sanctioned constructors and the GREEN-negative non-scope "+
			"constructions must add 0) — over/under-detection regression or fixture "+
			"set changed", runScopeConstructorFunnelRuleID, got, wantViolations)
	}
}

// containsStructName reports whether msg attributes the diagnostic to the scope
// struct name. The diagnostic embeds the name as "RunScope <name> outside", so
// the " outside" suffix anchors an exact-token match — avoiding a substring
// collision between "typedRunScope" and a hypothetical longer name.
func containsStructName(msg, name string) bool {
	return strings.Contains(msg, name+" outside")
}
