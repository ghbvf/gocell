// INVARIANT: RECONCILE-RESULT-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the set of string values assigned to the
// unexported result* consts in kernel/reconcile — the value set for the
// `result` label on reconcile_total — is frozen to exactly:
//
//	{"success", "transient", "permanent", "skipped"}
//
// A recovered reconciler panic is classified "transient" (retryable). There is
// NO 5th "panic" label. This is the FR-010 invariant: the label value set is
// design-time bounded; runtime drift (a 5th const, a renamed value) breaks
// dashboards and SLOs without a compile error.
//
// # AI-robust rating
//
// Medium. Mechanism: go/types const-value enumeration + order-insensitive
// comparison against an independent hardcoded want-set. It is not Hard because
// the consts are package-private (`result*` names), so Go's type system cannot
// express "this set is sealed at compile time" from outside — an in-package
// author can add a new const and the compiler does not object. The archtest
// provides the external frozen-witness.
//
// Hard upgrade path: enroll the result value set into the metricschema golden
// so a value-freeze is byte-locked at codegen time. That would make "adding a
// 5th result value without updating the golden" a codegen-diff CI failure
// (Hard). Tracked at gh #1416.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - This test enumerates consts by the sealed resultLabel TYPE (not by name
//     prefix) and by STRING kind, then compares their VALUES to the frozen set.
//     It does NOT verify that classify() actually returns one of the 4 values in
//     every code path — that behavioral invariant is covered by TestClassify
//     (kernel/reconcile unit tests) and TestRecovery_PanicMetricRecorded, not
//     here. "Value in const set but not enforced by classify" is out of scope.
//
//   - Two former blind spots are now CLOSED:
//     (a) "5th const renamed off the result prefix" — enumeration is by
//     resultLabel type identity, so a const named myFoo of type resultLabel
//     is still counted (and a 5th const still fails the count assertion).
//     (b) "inline string literal passed to recordResult" — recordResult's
//     result parameter is the sealed resultLabel type, and the downstream
//     callsite guard (scanRecordResultCallsites /
//     TestReconcileResultLabelValuesFrozen01_CallsiteGuard) bans any inline
//     constant argument (a string literal or resultLabel(...) conversion),
//     so the only values that can reach the metric are the declared consts
//     or a typed (non-constant) resultLabel from classify().
//
//   - The test only runs on the kernel/reconcile package Pass (filtered by
//     p.Pkg.Path()). A const named result* outside that package is not scanned.
//     That is intentional: result* are package-private by design (no exported
//     const "Result*" exists), so cross-package leakage is structurally impossible.
//
// # Reverse self-check (non-vacuous proof)
//
// TestReconcileResultLabelValuesFrozen01_NegativeControl demonstrates that a
// synthetic 5th value ("panic") or a renamed value ("done") is detected.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/types"
	"slices"
	"strings"
	"testing"
)

// wantResultLabelValues is the frozen membership of the reconcile_total `result`
// label value set. Updating this list requires reviewer attention and a
// simultaneous update to: (1) kernel/reconcile/metrics.go result* consts,
// (2) kernel/reconcile/recovery.go classify() mapping, (3) dashboards/alerts
// referencing reconcile_total{result=...}, and (4) the observability.md
// §"Reconcile Metrics result Label" doc契约.
//
// Semantic note — "skipped": a `skipped` result does NOT mean the trigger was
// silently dropped. It means the trigger arrived while the entity was in-flight
// (processing=true) and was coalesced into the dirty map; a follow-up reconcile
// at delay=0 is guaranteed after the in-flight run completes (F5 dirty re-run).
// Convergence is preserved: no trigger is ever lost.
var wantResultLabelValues = []string{
	"success",
	"transient",
	"permanent",
	"skipped",
}

// resultLabelType returns the kernel/reconcile package's sealed `resultLabel`
// named type, or (nil, false) if it is absent (renamed/removed). Enumerating
// the result value set BY TYPE (not by name prefix) makes the freeze rename-proof:
// a const renamed off the "result" prefix but still typed resultLabel is still
// counted, and a 5th resultLabel const is still caught.
func resultLabelType(p *Pass) (types.Type, bool) {
	if p.Pkg == nil {
		return nil, false
	}
	obj := p.Pkg.Scope().Lookup("resultLabel")
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil, false
	}
	return tn.Type(), true
}

// collectReconcileResultConsts enumerates the string constant values of every
// package-scope const in p (which must be the kernel/reconcile package) whose
// TYPE is the sealed resultLabel named type. Returns the collected string values.
func collectReconcileResultConsts(p *Pass) []string {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	labelType, ok := resultLabelType(p)
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

// resultValuesDiff returns "" when got and want hold the same set of values
// (order-insensitive), else a human-readable description of the missing/extra
// values. Mirrors reservedKeysDiff from outbox_reserved_metadata_keys_frozen_test.go.
func resultValuesDiff(got, want []string) string {
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

// TestReconcileResultLabelValuesFrozen01 freezes the result* const VALUE set in
// kernel/reconcile against the independent hardcoded wantResultLabelValues (anti-
// tautology). Both directions: nothing missing, nothing extra.
func TestReconcileResultLabelValuesFrozen01(t *testing.T) {
	t.Parallel()

	const reconcilePkg = PlatformModulePath + "/kernel/reconcile"
	var gotValues []string

	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != reconcilePkg {
			return nil
		}
		gotValues = collectReconcileResultConsts(p)
		return nil
	})

	if len(gotValues) == 0 {
		t.Fatalf("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01: found 0 result* string consts in %s — "+
			"did the package path change, or were the consts renamed/removed?", reconcilePkg)
	}

	if diff := resultValuesDiff(gotValues, wantResultLabelValues); diff != "" {
		t.Fatalf("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01: result* const value set in kernel/reconcile "+
			"drifted from the frozen want-set.\n%s\n"+
			"The result label value set for reconcile_total is frozen to {success,transient,permanent,skipped}. "+
			"A recovered panic is classified 'transient' (NOT a new 'panic' label). "+
			"If this change is intentional, update ALL sync points in the same PR: "+
			"(1) wantResultLabelValues here, (2) kernel/reconcile/recovery.go classify(), "+
			"(3) dashboards/alerts, (4) .claude/rules/gocell/observability.md §Reconcile Metrics result Label.", diff)
	}

	// Reverse self-check: assert exactly 4 result* consts exist (the same count as
	// the want-set). A 5th const (e.g. resultPanic) would produce an extra entry
	// AND increment this count.
	if len(gotValues) != len(wantResultLabelValues) {
		t.Errorf("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01: found %d result* string consts, want exactly %d "+
			"— a 5th result const was added without updating the golden; see wantResultLabelValues",
			len(gotValues), len(wantResultLabelValues))
	}
}

// scanRecordResultCallsites is the downstream callsite guard: it flags any
// (Metrics).recordResult call whose result argument (index 2) is a compile-time
// CONSTANT that is not a bare reference to a declared const. This closes the one
// hole the sealed resultLabel type cannot — Go assigns an untyped string literal
// to a defined string type, so recordResult(ctx, id, "panic") would otherwise
// compile and introduce a 5th label value invisibly. Allowed: a named const
// Ident (resultSuccess/…/resultSkipped — the freeze test bounds that set to 4),
// or any non-constant resultLabel expression (the `label` var from classify(),
// a classify() call). Banned: a string literal or a resultLabel("x") conversion.
func scanRecordResultCallsites(p *Pass) []Diagnostic {
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
			if !ok || sel.Sel.Name != "recordResult" {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != "recordResult" || len(call.Args) < 3 {
				return
			}
			arg := call.Args[2]
			tv, ok := info.Types[arg]
			if !ok || tv.Value == nil {
				return // non-constant (typed var / func call) — allowed
			}
			// A compile-time-constant value is reaching recordResult: allow ONLY a
			// bare Ident bound to a declared const (the freeze test caps that set).
			if id, isIdent := arg.(*ast.Ident); isIdent {
				if _, isConst := info.ObjectOf(id).(*types.Const); isConst {
					return
				}
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(arg.Pos()).Line,
				Message: "recordResult result argument is an inline constant — pass a declared " +
					"result* const (resultSuccess/resultTransient/resultPermanent/resultSkipped), " +
					"not a string literal or resultLabel(...) conversion " +
					"(RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 callsite guard)",
			})
		})
	}
	return diags
}

// TestReconcileResultLabelValuesFrozen01_CallsiteGuard is the production GREEN
// baseline for the downstream funnel: every recordResult call in kernel/reconcile
// passes a declared const or a typed (non-constant) resultLabel value — never an
// inline literal.
func TestReconcileResultLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	const reconcilePkg = PlatformModulePath + "/kernel/reconcile"
	var allDiags []Diagnostic
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != reconcilePkg {
			return nil
		}
		allDiags = append(allDiags, scanRecordResultCallsites(p)...)
		return nil
	})
	Report(t, "RECONCILE-RESULT-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestReconcileResultLabelValuesFrozen01_NegativeControl proves the comparison is
// non-vacuous: a synthetically drifted set (a 5th value added, one value renamed)
// MUST produce a non-empty diff (blind-spot self-check A per ai-robust.md).
func TestReconcileResultLabelValuesFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	// Extra value "panic" — the forbidden 5th label.
	withExtra := []string{"success", "transient", "permanent", "skipped", "panic"}
	if diff := resultValuesDiff(withExtra, wantResultLabelValues); diff == "" {
		t.Fatal("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 negative control: a set with extra 'panic' " +
			"produced an empty diff — the comparison is vacuous and would not catch a real drift")
	}

	// Missing value — "transient" renamed to "done".
	withMissing := []string{"success", "done", "permanent", "skipped"}
	if diff := resultValuesDiff(withMissing, wantResultLabelValues); diff == "" {
		t.Fatal("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 negative control: a set with 'transient' " +
			"replaced by 'done' produced an empty diff — the comparison is vacuous")
	}

	// Correct set must produce empty diff (over-fire guard).
	if diff := resultValuesDiff(wantResultLabelValues, wantResultLabelValues); diff != "" {
		t.Fatalf("RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 negative control: the frozen want-set "+
			"compared against itself produced a non-empty diff — the comparison has a bug: %s", diff)
	}
}

// TestReconcileResultLabelValuesFrozen01_CallsiteGuard_Fixtures proves the
// callsite guard is non-vacuous: the RED fixture (inline "panic" literal) is
// flagged, the GREEN fixture (named const + typed var) is not.
func TestReconcileResultLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
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
			pattern := "./tools/archtest/testdata/reconcile_result_callsite_fixtures/" + c.dir
			diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, scanRecordResultCallsites)
			if len(diags) != c.want {
				t.Fatalf("callsite-guard fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
