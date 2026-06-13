//go:build archtest

// INVARIANT: AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the set of string values assigned to the
// unexported pdpDecisionLabel consts in runtime/auth — the value set for the
// `decision` label on auth_pdp_decision_total / auth_pdp_decision_duration_seconds
// — is frozen to exactly:
//
//	{"allow", "deny", "error"}
//
// "allow"/"deny" are the PDP verdict; "error" is a fail-closed Authorize error
// (policy store down / tenant missing → 503) — kept distinct from a policy "deny"
// (403) so on-call can separate infra failure from authorization failure (#2027 F12).
//
// # AI-robust rating
//
// Hard (composed). The `decision` label is funneled by the TYPED parameter
// PDPMetrics.recordDecision(…, d pdpDecisionLabel, …): a bare string is not
// assignable to pdpDecisionLabel without a conversion, so the only constant
// values that can reach the metric are the declared consts. This file adds the
// two external witnesses that close the residual holes the type cannot:
//
//   - A1 (Medium): go/types const-value enumeration BY the pdpDecisionLabel type
//     (rename-proof) vs an independent hardcoded want-set.
//   - A2 (Hard): the downstream callsite guard bans any inline-constant decision
//     argument (a string literal OR a pdpDecisionLabel("x") conversion), so the
//     only values reaching recordDecision are declared consts or a non-constant
//     typed value (classifyDecision()'s return).
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - The `action` label is NOT frozen here. Its closure is upstream: action
//     values originate from the sealed authz.Permission type (newPermission is
//     unexported — the registry is the sole minter), so the action value-space is
//     bounded by the Permission registry. The observableAuthorizer decorator
//     forwards action as a plain string through the auth.Authorizer interface
//     boundary, where go/types cannot re-prove Permission provenance — so this
//     test does NOT add a (redundant, drift-prone) action freeze. Funnel strength:
//     upstream Permission seal = Hard; downstream decorator string-forward = not
//     separately guarded (declared blind spot).
//
//   - This test enumerates consts by the pdpDecisionLabel TYPE (not name prefix)
//     and by STRING kind. It does NOT assert classifyDecision returns one of the
//     three in every path — that behavior is covered by
//     TestObservableAuthorizer_RecordsDecision (runtime/auth unit tests).
//
//   - Only the runtime/auth package Pass is scanned (filtered by p.Pkg.Path()).
//     pdpDecisionLabel is package-private by design, so cross-package leakage is
//     structurally impossible.
//
// # Reverse self-check (non-vacuous proof)
//
// TestAuthzPDPDecisionLabelValuesFrozen01_NegativeControl demonstrates a
// synthetic 4th value or a renamed value is detected; the callsite-guard fixtures
// (red_literal / green) prove the downstream scan is non-vacuous.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/types"
	"slices"
	"strings"
	"testing"
)

// runtimeAuthPkg is the import path of the package that owns the pdpDecisionLabel
// type and the recordDecision funnel.
const runtimeAuthPkg = PlatformModulePath + "/runtime/auth"

// wantPDPDecisionLabelValues is the frozen membership of the auth_pdp_decision_*
// `decision` label value set. Updating this list requires reviewer attention and
// a simultaneous update to: (1) runtime/auth pdpDecisionLabel consts,
// (2) classifyDecision() mapping, (3) dashboards/alerts referencing
// auth_pdp_decision_total{decision=...}.
var wantPDPDecisionLabelValues = []string{
	"allow",
	"deny",
	"error",
}

// pdpDecisionLabelType returns runtime/auth's pdpDecisionLabel named type, or
// (nil, false) if absent (renamed/removed). Enumeration BY type (not name prefix)
// makes the freeze rename-proof.
func pdpDecisionLabelType(p *Pass) (types.Type, bool) {
	if p.Pkg == nil {
		return nil, false
	}
	tn, ok := p.Pkg.Scope().Lookup("pdpDecisionLabel").(*types.TypeName)
	if !ok {
		return nil, false
	}
	return tn.Type(), true
}

// collectPDPDecisionConsts enumerates the string constant values of every
// package-scope const in p (runtime/auth) whose TYPE is pdpDecisionLabel.
func collectPDPDecisionConsts(p *Pass) []string {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	labelType, ok := pdpDecisionLabelType(p)
	if !ok {
		return nil
	}
	scope := p.Pkg.Scope()
	var values []string
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		if !types.Identical(c.Type(), labelType) {
			continue
		}
		if c.Val().Kind() != constant.String {
			continue
		}
		values = append(values, constant.StringVal(c.Val()))
	}
	return values
}

// pdpDecisionValuesDiff returns "" when got and want hold the same value set
// (order-insensitive), else a human-readable extra/missing description. Mirrors
// resultValuesDiff (each freeze test owns its diff; sliceOrNone is shared).
func pdpDecisionValuesDiff(got, want []string) string {
	gotSorted := slices.Clone(got)
	wantSorted := slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if slices.Equal(gotSorted, wantSorted) {
		return ""
	}
	var extra, missing []string
	wantSet := make(map[string]struct{}, len(want))
	for _, k := range want {
		wantSet[k] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, k := range got {
		gotSet[k] = struct{}{}
		if _, ok := wantSet[k]; !ok {
			extra = append(extra, k)
		}
	}
	for _, k := range want {
		if _, ok := gotSet[k]; !ok {
			missing = append(missing, k)
		}
	}
	slices.Sort(extra)
	slices.Sort(missing)
	return "  extra (in production, not in golden):   " + sliceOrNone(extra) + "\n" +
		"  missing (in golden, not in production): " + sliceOrNone(missing)
}

// TestAuthzPDPDecisionLabelValuesFrozen01 freezes the pdpDecisionLabel const VALUE
// set against the independent hardcoded want-set (anti-tautology); both directions.
func TestAuthzPDPDecisionLabelValuesFrozen01(t *testing.T) {
	t.Parallel()

	var gotValues []string
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != runtimeAuthPkg {
			return nil
		}
		gotValues = collectPDPDecisionConsts(p)
		return nil
	})

	if len(gotValues) == 0 {
		t.Fatalf("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01: found 0 pdpDecisionLabel string consts in %s — "+
			"did the package path change, or was the type renamed/removed?", runtimeAuthPkg)
	}

	if diff := pdpDecisionValuesDiff(gotValues, wantPDPDecisionLabelValues); diff != "" {
		t.Fatalf("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01: pdpDecisionLabel const value set in runtime/auth "+
			"drifted from the frozen want-set.\n%s\n"+
			"The `decision` label value set is frozen to {allow,deny,error} ('error' = fail-closed Authorize "+
			"error, distinct from policy 'deny'). If intentional, update ALL sync points in the same PR: "+
			"(1) wantPDPDecisionLabelValues here, (2) runtime/auth classifyDecision(), (3) dashboards/alerts.", diff)
	}

	if len(gotValues) != len(wantPDPDecisionLabelValues) {
		t.Errorf("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01: found %d pdpDecisionLabel string consts, want exactly %d "+
			"— a value was added/removed without updating the golden; see wantPDPDecisionLabelValues",
			len(gotValues), len(wantPDPDecisionLabelValues))
	}
}

// scanRecordDecisionCallsites is the downstream callsite guard: it flags any
// (*PDPMetrics).recordDecision call whose decision argument (index 2) is a
// compile-time CONSTANT that is not a bare reference to a declared const. Allowed:
// a named const Ident (pdpDecisionAllow/Deny/Error — the freeze test bounds that
// set to 3), or any non-constant pdpDecisionLabel expression (classifyDecision()'s
// return). Banned: a string literal or a pdpDecisionLabel("x") conversion.
func scanRecordDecisionCallsites(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "recordDecision" {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != "recordDecision" || len(call.Args) < 3 {
				return
			}
			arg := call.Args[2] // the pdpDecisionLabel (decision) argument
			tv, ok := info.Types[arg]
			if !ok || tv.Value == nil {
				return // non-constant (typed var / classifyDecision() call) — allowed
			}
			if id, isIdent := arg.(*ast.Ident); isIdent {
				if _, isConst := info.ObjectOf(id).(*types.Const); isConst {
					return // declared const — allowed (freeze test caps that set to 3)
				}
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(arg.Pos()).Line,
				Message: "recordDecision decision argument is an inline constant — pass a declared " +
					"pdpDecision* const (pdpDecisionAllow/pdpDecisionDeny/pdpDecisionError), " +
					"not a string literal or pdpDecisionLabel(...) conversion " +
					"(AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 callsite guard)",
			})
		})
	}
	return diags
}

// TestAuthzPDPDecisionLabelValuesFrozen01_CallsiteGuard is the production GREEN
// baseline for the downstream funnel: every recordDecision call in runtime/auth
// passes a declared const or a typed (non-constant) value — never an inline literal.
func TestAuthzPDPDecisionLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != runtimeAuthPkg {
			return nil
		}
		allDiags = append(allDiags, scanRecordDecisionCallsites(p)...)
		return nil
	})

	Report(t, "AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestAuthzPDPDecisionLabelValuesFrozen01_NegativeControl proves the comparison is
// non-vacuous (blind-spot self-check A per ai-robust.md).
func TestAuthzPDPDecisionLabelValuesFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	withExtra := []string{"allow", "deny", "error", "skipped"}
	if diff := pdpDecisionValuesDiff(withExtra, wantPDPDecisionLabelValues); diff == "" {
		t.Fatal("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 negative control: a set with extra 'skipped' " +
			"produced an empty diff — the comparison is vacuous and would not catch a real drift")
	}

	withMissing := []string{"allow", "deny", "degraded"}
	if diff := pdpDecisionValuesDiff(withMissing, wantPDPDecisionLabelValues); diff == "" {
		t.Fatal("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 negative control: 'error' renamed to 'degraded' " +
			"produced an empty diff — the comparison is vacuous")
	}

	if diff := pdpDecisionValuesDiff(wantPDPDecisionLabelValues, wantPDPDecisionLabelValues); diff != "" {
		t.Fatalf("AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 negative control: the frozen want-set compared "+
			"against itself produced a non-empty diff — the comparison has a bug: %s", diff)
	}
}

// TestAuthzPDPDecisionLabelValuesFrozen01_CallsiteGuard_Fixtures proves the
// callsite guard is non-vacuous: the RED fixture (inline "deny" literal) is
// flagged, the GREEN fixture (named const + typed var) is not.
func TestAuthzPDPDecisionLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		want int
	}{
		{"red_literal", 1},
		{"green", 0},
	}
	for _, c := range cases {
		c := c
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/authz_pdp_decision_callsite_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), scanRecordDecisionCallsites)
			if len(diags) != c.want {
				t.Fatalf("callsite-guard fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
