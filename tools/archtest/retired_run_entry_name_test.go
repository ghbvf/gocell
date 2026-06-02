package archtest

// INVARIANT: ARCHTEST-RETIRED-RUN-NAME-01
//
// ARCHTEST-RETIRED-RUN-NAME-01 — the deleted Run* entry-point names
// (RunTyped / RunTypedProduction / RunTypedDir / RunTypedFixture, collapsed
// into the single [Run] + sealed [RunScope] in issue #1037 §1d) must not
// reappear as a CURRENT name in the tools/archtest façade. The trigger was
// TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01, a rule whose ID outlived the RunTyped API
// it was named after; this guard makes that drift fail CI instead of lingering.
//
//   - A1 (tool name): no top-level function may be DECLARED with the retired
//     "RunTyped" prefix. Closes the gap left by the exported-only guards
//     FACADE-CONTRACTED-EXPORTS-01 (re-export ban) and ARCHTEST-SINGLE-RUN-
//     ENTRY-01 (exported Run* ban) — those two never see an UNexported
//     `func RunTyped(...)` re-declaration.
//   - A2 (rule name): no string literal SHAPED like a rule ID (SCREAMING-KEBAB
//     ending in -NN) may carry the retired all-caps token "RUNTYPED". Catches a
//     new rule named e.g. FOO-RUNTYPED-01 leaking the deleted API name into a
//     `const xRuleID = "..."` / `Report(t, "...", …)` argument.
//
// Historical backstory that legitimately names the deleted RunTyped API lives
// in `//` comments (e.g. taggroup_loop_no_typed_run_invariants.go) and is OUT
// of scope by construction: comments are neither declarations nor string
// literals, and the backstory uses the camelCase "RunTyped" form, never the
// all-caps rule-ID "RUNTYPED" token A2 keys on.
//
// # AI-robust: Medium
//
// AST-name + string-literal-value match (no go/types). Go cannot make
// re-declaring an unexported `func RunTyped` or writing a "RUNTYPED" rule-ID
// literal a compile error, so this is an archtest backstop, not a type-system
// Hard. It composes with the Hard exported-surface guards above to cover the
// unexported-identifier + rule-ID-string vectors those two miss. There is no
// caller allowlist and no comment-anchor escape.
//
// # Blind spots (per ai-robust.md Medium evidence requirement)
//
//   - A2 flags only rule-ID-SHAPED literals (retiredRuleIDShape). A bare
//     "RUNTYPED" substring in free-form prose / a message string is
//     intentionally allowed — that is exactly what keeps this file's own
//     denylist token from self-tripping; hiding a rule ID in a non-rule-shaped
//     string is not a construction that reaches Report, so it is out of scope.
//   - A2 does not scan `// INVARIANT:` comments directly. Every rule ID in this
//     codebase also surfaces as a string literal (`const xRuleID` /
//     `Report(t, "…")`); an INVARIANT comment with no backing literal enforces
//     nothing and is therefore not a real bypass.
//   - A1 scans only top-level FuncDecl names. A retired name re-introduced as a
//     type / var / const is caught for the EXPORTED case by
//     FACADE-CONTRACTED-EXPORTS-01; the unexported type/var/const residue is
//     accepted (it is not a "tool name" a caller can dispatch through Run).
//
// Reverse self-check: TestArchtestRetiredRunName_PredicateNonVacuous asserts the
// two predicates fire on synthetic retired names AND do NOT fire on the
// surviving entry points (Run / RunScope / RunStandardCellRules / Typed /
// Production) or the renamed rule ID, so the live scan cannot pass vacuously.

import (
	"go/ast"
	"regexp"
	"strings"
	"testing"
)

// retiredRunFuncPrefix is the shared prefix of every deleted Run* entry point
// (RunTyped, RunTypedProduction, RunTypedDir, RunTypedFixture). A top-level
// function declared with this prefix re-introduces a retired tool name.
const retiredRunFuncPrefix = "RunTyped"

// retiredRuleToken is the all-caps form the retired name takes inside a
// SCREAMING-KEBAB rule ID. Held as a bare (non-rule-shaped) constant so this
// very file does not self-trip A2.
const retiredRuleToken = "RUNTYPED"

// retiredRuleIDShape matches a GoCell rule ID: SCREAMING-KEBAB segments ending
// in a two-digit ordinal (e.g. TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01). It does NOT
// match a bare token like "RUNTYPED" (no hyphen group, no -NN suffix), nor the
// camelCase identifier denylist strings ("RunTyped") that FACADE-CONTRACTED-
// EXPORTS-01 legitimately carries.
var retiredRuleIDShape = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+-[0-9]{2}$`)

// hasRetiredRunFuncPrefix reports whether a declared function name re-introduces
// a retired Run* entry point. "Run", "RunScope", "RunStandardCellRules" survive
// (none carries the "RunTyped" prefix).
func hasRetiredRunFuncPrefix(name string) bool {
	return strings.HasPrefix(name, retiredRunFuncPrefix)
}

// isRetiredRuleID reports whether s is a rule-ID-shaped string carrying the
// retired all-caps RunTyped token.
func isRetiredRuleID(s string) bool {
	return retiredRuleIDShape.MatchString(s) && strings.Contains(s, retiredRuleToken)
}

// TestArchtestRetiredRunName enforces ARCHTEST-RETIRED-RUN-NAME-01 over the
// tools/archtest façade direct-child .go files (test + non-test), AST-only — no
// go/types load, so the scan is a cheap parser pass.
func TestArchtestRetiredRunName(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/archtest"}, IncludeTests(),
		MatchRels(func(rel string) bool {
			slash := strings.LastIndex(rel, "/")
			if slash < 0 {
				return false
			}
			// Direct children of tools/archtest only (exclude internal/* sub-packages).
			return rel[:slash] == "tools/archtest" && strings.HasSuffix(rel[slash+1:], ".go")
		}))

	// Non-vacuity guard: record that the scope actually visited the core façade
	// file. If MatchRels ever filters everything out, the scan would pass
	// vacuously; this fails loudly instead.
	sawCoreFacadeFile := false

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if rel == "tools/archtest/pass.go" {
				sawCoreFacadeFile = true
			}
			// A1: retired Run* prefix on a top-level function declaration.
			EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Recv != nil || fn.Name == nil {
					return
				}
				if hasRetiredRunFuncPrefix(fn.Name.Name) {
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(fn.Name.Pos()).Line,
						Message: "function " + fn.Name.Name + " reintroduces a retired Run* entry-point " +
							"name (RunTyped/RunTypedProduction/RunTypedDir/RunTypedFixture were deleted in " +
							"#1037 §1d); dispatch via Run(t, <RunScope>, rule) with a Typed/Production/Fixture/" +
							"StandaloneModule constructor instead — ARCHTEST-RETIRED-RUN-NAME-01",
					})
				}
			})
			// A2: retired RUNTYPED token inside a current rule-ID string literal.
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok {
					return
				}
				if isRetiredRuleID(val) {
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: "rule ID " + val + " carries the retired RunTyped token; the rule was " +
							"renamed away from RunTyped when the API collapsed in #1037 §1d (e.g. " +
							"TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01) — ARCHTEST-RETIRED-RUN-NAME-01",
					})
				}
			})
		}
		return out
	})

	Report(t, "ARCHTEST-RETIRED-RUN-NAME-01", diags)

	if !sawCoreFacadeFile {
		t.Fatal("ARCHTEST-RETIRED-RUN-NAME-01 non-vacuity: scope did not visit " +
			"tools/archtest/pass.go — the façade scope is empty or misconfigured, so the " +
			"scan would pass vacuously")
	}
}

// TestArchtestRetiredRunName_PredicateNonVacuous proves the two predicates are
// non-vacuous: they fire on synthetic retired names and do NOT fire on the
// surviving entry points or the renamed rule ID. The synthetic bad rule ID is
// assembled from parts so no rule-ID-shaped "RUNTYPED" literal exists in this
// file (which would self-trip the live A2 scan above).
func TestArchtestRetiredRunName_PredicateNonVacuous(t *testing.T) {
	t.Parallel()
	syntheticBadRuleID := "FOO-" + retiredRuleToken + "-01"

	// A1 predicate fires on a retired name, not on survivors.
	if !hasRetiredRunFuncPrefix("RunTypedProduction") {
		t.Error("A1 predicate vacuous: RunTypedProduction was not flagged")
	}
	for _, surviving := range []string{"Run", "RunScope", "RunStandardCellRules", "Typed", "Production"} {
		if hasRetiredRunFuncPrefix(surviving) {
			t.Errorf("A1 predicate false positive: surviving entry %q was flagged", surviving)
		}
	}

	// A2 predicate fires on a retired rule ID, not on the renamed rule, the bare
	// token, or the camelCase identifier denylist string.
	if !isRetiredRuleID(syntheticBadRuleID) {
		t.Errorf("A2 predicate vacuous: %q was not flagged", syntheticBadRuleID)
	}
	for _, fine := range []string{
		"TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01", // the renamed rule ID
		retiredRuleToken,                     // bare token, not rule-ID-shaped
		"RunTyped",                           // camelCase identifier denylist string
	} {
		if isRetiredRuleID(fine) {
			t.Errorf("A2 predicate false positive: %q was flagged", fine)
		}
	}
}
