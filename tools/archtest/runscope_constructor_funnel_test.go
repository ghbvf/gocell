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
// type-aware CI backstop — it resolves each composite literal's type via
// *types.Info (p.TypesInfo.TypeOf) to a *types.Named whose object is one of the
// five scope structs in package archtest, then asserts the enclosing top-level
// FuncDecl (via ResolveEnclosingFunc) is one of the five sanctioned
// constructors. Name matching alone is insufficient (same-package construction
// is a bare Ident, not a qualified selector), so the rule is types-bound, not
// string-bound — an import alias / rename cannot bypass it.
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
//   - New named type derived from a scope struct (`type x typedRunScope;
//     x{}`): NOT detected — but harmless, because the method set of a struct is
//     NOT carried to a new named type, so x does not implement isRunScope() and
//     cannot reach Run. (A type ALIAS `type x = typedRunScope` IS detected:
//     TypeOf(x{}) resolves to the identical named typedRunScope.)
//   - Pointer-elided literal `&typedRunScope{}`: IS detected — the inner
//     CompositeLit node is typed `typedRunScope` (not `*typedRunScope`), so
//     TypeOf(comp) resolves it. Even were it missed, isRunScope() has a VALUE
//     receiver, so `*typedRunScope` does not implement RunScope and
//     `Run(t, &typedRunScope{}, rule)` is a compile error — a type-system
//     backstop. Not a bypass.
//   - Reflect / runtime construction: outside Go static AST + types.Info scope,
//     same accepted boundary as the entire archtest framework.
//   - A scope struct returned from a non-constructor helper that is itself
//     wrapped by a constructor: not applicable — the five structs have no
//     factory other than their five constructors, and adding one is exactly the
//     change this rule's per-member fixture lock would force into the allowlist.
//
// # Reverse self-check
//
//   - TestRunScopeConstructorFunnel01_FixtureCoverage loads the archtest_fixture
//     in-package RED fixture (runscope_ctor_redfixture.go) and asserts each of
//     the five struct names is flagged (per-member trip-wire) AND that the total
//     count equals exactly five — so the five sanctioned constructors NOT being
//     flagged is load-bearing (a constructor wrongly flagged would push the
//     count above five).
//   - TestRunScopeConstructorFunnel01 (the live gate) asserts production package
//     archtest (no fixture tag) has ZERO violations — every scope-struct literal
//     in pass.go / fixture.go is inside its constructor.
package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const runScopeConstructorFunnelRuleID = "RUNSCOPE-CONSTRUCTOR-FUNNEL-01"

// runScopeStructNames is the set of sealed RunScope struct names whose
// composite-literal construction is funneled to the sanctioned constructors.
// Adding a sixth RunScope struct MUST add it here (and a sanctioned constructor
// to runScopeSanctionedCtors) or the new struct is constructible anywhere
// in-package — the red fixture's per-member trip-wire forces this maintenance.
var runScopeStructNames = map[string]struct{}{
	"astRunScope":        {},
	"typedRunScope":      {},
	"productionRunScope": {},
	"dirRunScope":        {},
	"fixtureRunScope":    {},
}

// runScopeSanctionedCtors is the set of top-level constructor func names allowed
// to construct a RunScope struct literal. These are the five typed RunScope
// constructors (pass.go: AST / Typed / Production / StandaloneModule; fixture.go:
// Fixture).
var runScopeSanctionedCtors = map[string]struct{}{
	"AST":              {},
	"Typed":            {},
	"Production":       {},
	"StandaloneModule": {},
	"Fixture":          {},
}

// scanRunScopeConstructorViolations walks file for composite literals of the
// five sealed RunScope structs and reports each one not lexically inside a
// sanctioned constructor body.
func scanRunScopeConstructorViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var out []Diagnostic
	EachInSubtree[ast.CompositeLit](file, func(comp *ast.CompositeLit) {
		name, ok := runScopeStructLiteralName(p, comp)
		if !ok {
			return
		}
		fn, found := ResolveEnclosingFunc(p.TypesInfo, file, comp)
		// archtestPkgPath ("github.com/ghbvf/gocell/tools/archtest") is the
		// shared meta-archtest const declared in pass_funnel_test.go (same
		// package archtest test binary).
		if found && fn.Pkg() != nil && fn.Pkg().Path() == archtestPkgPath {
			if _, sanctioned := runScopeSanctionedCtors[fn.Name()]; sanctioned {
				return
			}
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(comp.Pos()).Line,
			Message: "composite literal " + name + "{…} constructs a sealed RunScope " +
				"outside its sanctioned constructor; build the scope via " +
				"archtest.{AST,Typed,Production,StandaloneModule,Fixture} and pass it " +
				"to Run(t, <RunScope>, rule) — RUNSCOPE-CONSTRUCTOR-FUNNEL-01",
		})
	})
	return out
}

// runScopeStructLiteralName resolves comp's type via *types.Info to a named
// struct in package archtest and returns its name when it is one of the five
// sealed RunScope structs.
func runScopeStructLiteralName(p *Pass, comp *ast.CompositeLit) (string, bool) {
	t := p.TypesInfo.TypeOf(comp)
	if t == nil {
		return "", false
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	if named.Obj().Pkg().Path() != archtestPkgPath {
		return "", false
	}
	name := named.Obj().Name()
	if _, ok := runScopeStructNames[name]; ok {
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
	diags := Run(t, Typed(TypedOpts{Tests: true}, []string{"./tools/archtest"}),
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
// The scope-struct literals seen are: the five sanctioned constructors (NOT
// flagged) + the five RED fixture constructions (flagged). The GREEN-negative
// fixture (runScopeConstructorGreenNegatives: TypedOpts{}/FixtureOpts{}/
// Diagnostic{} outside any ctor) MUST add zero — so the exact-count==5 lock is
// also a false-positive guard: a non-scope name mistakenly added to
// runScopeStructNames would trip a GREEN line and push the count past five.
func TestRunScopeConstructorFunnel01_FixtureCoverage(t *testing.T) {
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{"./tools/archtest"}),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				out = append(out, scanRunScopeConstructorViolations(p, file, p.Rel(file))...)
			}
			return out
		})

	perStruct := make(map[string]int, len(runScopeStructNames))
	for _, d := range diags {
		for name := range runScopeStructNames {
			if containsStructName(d.Message, name) {
				perStruct[name]++
			}
		}
	}
	missing := make([]string, 0)
	for name := range runScopeStructNames {
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
	// Exact-count lock: exactly five RED constructions, and the five sanctioned
	// constructors + the three GREEN-negative non-scope literals must add zero.
	// Drift (a constructor wrongly flagged, a non-scope name wrongly added to
	// runScopeStructNames, or a new RED construction without updating this count)
	// fails here.
	const wantViolations = 5
	if got := len(diags); got != wantViolations {
		t.Errorf("%s FixtureCoverage: %d violations, want %d "+
			"(five RED fixture constructions trip once each; the five sanctioned "+
			"constructors and the GREEN-negative non-scope literals must add 0) — "+
			"over-detection regression or fixture set changed",
			runScopeConstructorFunnelRuleID, got, wantViolations)
	}
}

// containsStructName reports whether msg embeds the struct name as the
// "<name>{…}" token the diagnostic uses. The "{" suffix avoids a substring
// collision between "typedRunScope" and a hypothetical longer name.
func containsStructName(msg, name string) bool {
	return strings.Contains(msg, name+"{")
}
