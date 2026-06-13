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
//     type, so recordOutcome(ctx, "event", "published", n) would compile and reach
//     the metric label bypassing the enum consts the golden froze. The callsite
//     guard bans any compile-time-constant kind/outcome argument to recordOutcome
//     that is not a bare reference to a declared const, so the ONLY values that can
//     reach outbox_relayed_total are the declared entryKind/relayOutcome consts (or
//     a non-constant typed expression). Upstream freezes the SET; downstream forces
//     every emission THROUGH that set. ("只锁 callsite 不是闭环 funnel": the golden is
//     the other half.)
//
// # AI-robust rating
//
// Medium for the callsite guard (go/types const-value classification + method-call
// resolution; the "which argument shapes may reach a method param" axis is not
// expressible in the Go type system, matching SAGA-/RECONCILE-RESULT-LABEL-VALUES-
// FROZEN-01). The freeze axis itself is Hard via the metricschema golden (upstream).
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
// "command"/"published" literals) yields 2 diagnostics; green (declared consts +
// typed vars) yields 0.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const outboxRelayLabelValuesPkg = PlatformModulePath + "/kernel/outbox"

// scanRecordOutcomeCallsites flags any (providerRelayCollector).recordOutcome call
// whose kind argument (index 1) or outcome argument (index 2) is a compile-time
// CONSTANT that is not a bare reference to a declared const. Allowed: a named const
// Ident (the freeze test / golden bound those sets) or any non-constant typed
// expression (a relayOutcome/entryKind var). Banned: a string literal or a
// entryKind("x") / relayOutcome("x") conversion.
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
			// Guard the kind (1) and outcome (2) label-value positions.
			for _, idx := range []int{1, 2} {
				arg := call.Args[idx]
				tv, ok := info.Types[arg]
				if !ok || tv.Value == nil {
					continue // non-constant (typed var / call) — allowed
				}
				if id, isIdent := arg.(*ast.Ident); isIdent {
					if _, isConst := info.ObjectOf(id).(*types.Const); isConst {
						continue
					}
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(arg.Pos()).Line,
					Message: "recordOutcome kind/outcome argument is an inline constant — pass a declared " +
						"entryKind/relayOutcome const (kindEvent/kindCommand, outcomePublished/…), not a string " +
						"literal or entryKind(...)/relayOutcome(...) conversion " +
						"(OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard)",
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
		{"red_literal", 2},
		{"green", 0},
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
