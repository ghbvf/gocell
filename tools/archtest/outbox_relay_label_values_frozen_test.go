//go:build archtest

// INVARIANT: OUTBOX-RELAY-LABEL-VALUES-FROZEN-01
//
// This file owns the DOWNSTREAM half of the funnel that freezes the {kind, outcome}
// label value sets of outbox_relayed_total (#1674):
//
//   - UPSTREAM (Hard, NOT in this file): the value sets are single-sourced from the
//     sealed entryKind / relayOutcome enum consts in kernel/outbox and statically
//     resolved by tools/metricschema into assemblies/corebundle/generated/
//     metrics-schema.yaml. Adding / renaming / removing an enum value changes the
//     golden; `gocell verify generated` then fails CI until the golden is
//     regenerated, and the anti-tautology witness
//     tools/metricschema/schema_test.go::TestBuild_CorebundleCapturesReachableTypedMetrics
//     pins the resolved {kind,outcome} set against an independent hardcoded want-set
//     (and the help against kernel/outbox.RelayedHelp). That is the Hard byte-lock —
//     a value cannot drift silently. (gh #1416 generalizes this golden value-freeze
//     to the saga/reconcile enums, retiring their A1 archtests.)
//
//   - DOWNSTREAM (Medium, THIS file): the sealed enum TYPE alone cannot stop an
//     untyped string literal — Go assigns an untyped constant to a defined string
//     type, so recordOutcome(ctx, "event", "published", n) (or an untyped/local
//     const) would compile and reach the metric label bypassing the enum consts the
//     golden froze. The callsite guard (mirroring WEBHOOK-METRIC-LABEL-VALUES-
//     FROZEN-01) allows a kind/outcome argument ONLY when it is a const whose type is
//     types.Identical to the position's enum param type (entryKind / relayOutcome,
//     resolved from the recordOutcome signature) AND declared at PACKAGE scope — i.e.
//     exactly the consts metricschema's resolveEnumStringConsts enumerated into the
//     golden (it scans package-scope typed consts only). String literals, untyped
//     consts, wrong-typed consts, function-local typed consts, and T(...) conversions
//     are all flagged; a non-constant typed value (a relayOutcome var) is allowed.
//     guard-allowed ≡ golden-frozen BY CONSTRUCTION — upstream freezes the SET,
//     downstream forces every emission THROUGH it. ("只锁 callsite 不是闭环 funnel":
//     the golden is the other half.) NOTE this is one notch tighter than the webhook
//     precedent (which omits the package-scope clause): outbox's golden freezes
//     package-scope consts, so the guard's exact dual requires package scope.
//
// # AI-robust rating
//
// Medium for the callsite guard (go/types const-value classification + method-call
// resolution; the "which argument shapes may reach a method param" axis is not
// expressible in the Go type system, matching SAGA-/RECONCILE-/WEBHOOK-RESULT-LABEL-
// VALUES-FROZEN). The freeze axis itself is Hard via the metricschema golden (upstream).
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - The guard binds on the method NAME recordOutcome resolved via go/types, in the
//     kernel/outbox package only. A future second emitter of the kind/outcome labels
//     NOT routed through recordOutcome would be outside this scan — but RecordPollCycle
//     is the sole producer and recordOutcome the sole emit point (sealed by the
//     unexported provider type), so a second raw emitter is review-visible.
//   - It does NOT verify the golden actually contains the enum values — that is the
//     upstream metricschema test's job (schema_test.go), deliberately separate.
//   - The upstream resolution is ONE-HOP (tools/metricschema importedTypesPackage):
//     the metric ctor's caller package must DIRECTLY import kernel/outbox. Today
//     cellmodules/configcore does; if a future caller reaches the ctor only through
//     an intermediate package, metricschema resolves empty and fails generation loud
//     (not a silent freeze hole) — but the value set would then be unfrozen until the
//     resolver is extended to a transitive walk. Accepted: review-visible + fail-loud.
//
// # Reverse self-check (non-vacuous proof)
//
// TestOutboxRelayLabelValuesFrozen01_CallsiteGuard_Fixtures: red_literal (inline
// "command"/"published" literals) → 2 diagnostics; red_untyped_const (an untyped
// const in the outcome position — proves the types.Identical clause) → 1; red_local_
// const (a function-local typed relayOutcome const — proves the package-scope clause)
// → 1; green (package-scope declared consts + typed vars) → 0.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const outboxRelayLabelValuesPkg = PlatformFrameworkModulePath + "/kernel/outbox"

// scanRecordOutcomeCallsites flags any (providerRelayCollector).recordOutcome call
// whose kind argument (index 1) or outcome argument (index 2) is a compile-time
// CONSTANT that is not a PACKAGE-SCOPE const whose type is types.Identical to the
// position's enum param type (entryKind / relayOutcome). Allowed: such a frozen-enum
// const, or any non-constant typed expression (a relayOutcome/entryKind var). Banned:
// a string literal, an untyped const, a wrong-typed const, a function-local typed
// const, or a entryKind("x")/relayOutcome("x") conversion — none of which the golden
// froze. Mirrors webhook scanEnumLabelCallsite + a package-scope clause (see godoc).
func scanRecordOutcomeCallsites(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "recordOutcome" {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != "recordOutcome" || len(call.Args) < 3 {
				return
			}
			sig, sigOK := fn.Type().(*types.Signature)
			if !sigOK || sig.Params().Len() < 3 {
				return
			}
			// Guard the kind (1) and outcome (2) label-value positions. The enum type
			// for each position is resolved from the recordOutcome signature
			// (entryKind / relayOutcome) — robust for both the production package and
			// the fixtures, which declare their own local enum types.
			for _, idx := range []int{1, 2} {
				arg := call.Args[idx]
				tv, ok := info.Types[arg]
				if !ok || tv.Value == nil {
					continue // non-constant typed value (a relayOutcome var) — allowed
				}
				enumType := sig.Params().At(idx).Type()
				if obj := constObjectOf(info, arg); obj != nil {
					if c, isConst := obj.(*types.Const); isConst &&
						types.Identical(c.Type(), enumType) &&
						c.Pkg() != nil && c.Parent() == c.Pkg().Scope() {
						// A PACKAGE-SCOPE const of the sealed enum type — exactly the set
						// metricschema's resolveEnumStringConsts froze into the golden
						// (it enumerates package-scope typed consts only). This makes
						// guard-allowed ≡ golden-frozen by construction.
						continue
					}
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(arg.Pos()).Line,
					Message: "recordOutcome " + enumType.String() + " argument is a constant that is not a " +
						"package-scope declared " + enumType.String() + " const — pass a frozen enum const " +
						"(kindEvent/kindCommand, outcomePublished/…) or a typed non-constant value, never a " +
						"string literal, an untyped const, a function-local const, or an " + enumType.String() +
						"(...) conversion (OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard)",
				})
			}
		})
	}
	return diags
}

// TestOutboxRelayLabelValuesFrozen01_CallsiteGuard is the production GREEN baseline:
// every recordOutcome call in kernel/outbox passes declared consts (or a typed
// non-constant), never an inline literal.
func TestOutboxRelayLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != outboxRelayLabelValuesPkg {
			return nil
		}
		allDiags = append(allDiags, scanRecordOutcomeCallsites(p)...)
		return nil
	})

	Report(t, "OUTBOX-RELAY-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestOutboxRelayLabelValuesFrozen01_CallsiteGuard_Fixtures proves the guard is
// non-vacuous: the RED fixture (inline kind+outcome literals) is flagged twice, the
// GREEN fixture (declared consts + typed vars) is not.
func TestOutboxRelayLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		want int
	}{
		{"red_literal", 2},       // two inline string literals (kind + outcome)
		{"red_untyped_const", 1}, // untyped const in outcome position (types.Identical clause)
		{"red_local_const", 1},   // function-local typed const (package-scope clause)
		{"green", 0},             // package-scope frozen-enum consts + typed vars
	}
	for _, c := range cases {
		c := c
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/outbox_relay_label_values_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), scanRecordOutcomeCallsites)
			if len(diags) != c.want {
				t.Fatalf("callsite-guard fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
