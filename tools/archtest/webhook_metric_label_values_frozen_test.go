// INVARIANT: WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the value sets of the two sealed string-typed
// label enums in kernel/webhook — the `result` label on webhook_deliveries_total
// and the `reason` label on webhook_signature_failures_total — are frozen to
// exactly:
//
//	webhookDeliveryResult  → {success, client_error, server_error, transport_error, blocked}
//	SignatureFailureReason → {missing_header, invalid_header, unknown_source, bad_signature, timestamp_expired}
//
// The delivery result is status-aware (NOT a 3-way disposition collapse): the
// kernel Classify maps every non-2xx + transient transport to Requeue, but the
// metric distinguishes 4xx (client_error) from 5xx (server_error) so an endpoint
// misconfiguration flood and a downstream outage are separable on dashboards.
// This mirrors Alertmanager's clientError/serverError split, Kubernetes admission
// webhook's error_type, and Convoy's Discarded. Runtime drift (a 6th const, a
// renamed value) would break dashboards/SLOs without a compile error.
//
// # AI-robust rating
//
// Medium. Mechanism: go/types const-value enumeration by sealed TYPE +
// order-insensitive comparison against an independent hardcoded want-set, plus a
// downstream callsite guard on recordDelivery. Not Hard because the enum consts
// are package-scoped string types (an in-package author can add a new const and
// the compiler does not object); the archtest is the external frozen-witness.
//
// Hard upgrade path: enroll both value sets into the metricschema golden so a
// value-freeze is byte-locked at codegen time (a 6th value without a golden
// update → codegen-diff CI failure). Shared with saga/reconcile at gh #1416 —
// do NOT open a duplicate tracking issue.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Enumeration is by enum TYPE identity (types.Identical), not by name prefix:
//     a const renamed off the Reason*/delivery* convention but still typed as the
//     enum is still counted, and a new const of the enum type is still caught.
//
//   - The callsite guard covers recordDelivery (the delivery-result enum mapped
//     from a switch in Dispatcher.Handle). It does NOT cover RecordSignatureFailure:
//     that exported method's reason argument originates from runtime/webhook's
//     verify step, which RETURNS a typed SignatureFailureReason — the type system
//     already constrains the value, and the cross-package callsite would require a
//     separate scan of runtime/webhook. The residual hole (a SignatureFailureReason("x")
//     conversion at a RecordSignatureFailure callsite) is a bounded blind spot, and is
//     closed by a reverse self-check: TestWebhookMetricLabelValuesFrozen01_NoReasonConversion
//     asserts zero SignatureFailureReason(...) conversions exist in production
//     kernel/webhook + runtime/webhook (the value-set freeze A1 catches new declared
//     consts; the conversion form is forbidden outright).
//
//   - The test runs only on the kernel/webhook package Pass (filtered by path).
//
// # Reverse self-check (non-vacuous proof)
//
// TestWebhookMetricLabelValuesFrozen01_NegativeControl demonstrates a synthetic
// extra/renamed value is detected; the callsite-guard fixture test proves the
// recordDelivery guard flags an inline literal and passes a declared const.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"testing"
)

// webhookLabelEnumWant is the frozen membership of the two webhook metric label
// value sets, keyed by the sealed enum type name in kernel/webhook. Updating any
// entry requires a simultaneous update to: (1) this map, (2) the enum consts in
// kernel/webhook/metrics.go, (3) dashboards/alerts referencing
// webhook_deliveries_total{result} / webhook_signature_failures_total{reason},
// and (4) the PR-6 observability documentation.
var webhookLabelEnumWant = map[string][]string{
	"webhookDeliveryResult": {
		"success", "client_error", "server_error", "transport_error", "blocked",
	},
	"SignatureFailureReason": {
		"missing_header", "invalid_header", "unknown_source", "bad_signature", "timestamp_expired",
	},
}

// collectWebhookEnumConsts enumerates the string constant values of every
// package-scope const in p whose TYPE is the named enum typeName. Returns the
// values and whether the type was found.
func collectWebhookEnumConsts(p *Pass, typeName string) ([]string, bool) {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil, false
	}
	tn, ok := p.Pkg.Scope().Lookup(typeName).(*types.TypeName)
	if !ok {
		return nil, false
	}
	labelType := tn.Type()
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
	return values, true
}

// TestWebhookMetricLabelValuesFrozen01 freezes both enum VALUE sets in
// kernel/webhook against the independent hardcoded want-sets (anti-tautology),
// both directions: nothing missing, nothing extra.
func TestWebhookMetricLabelValuesFrozen01(t *testing.T) {
	t.Parallel()

	const webhookPkg = PlatformModulePath + "/kernel/webhook"
	got := map[string][]string{}
	found := map[string]bool{}

	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != webhookPkg {
			return nil
		}
		for typeName := range webhookLabelEnumWant {
			if vals, ok := collectWebhookEnumConsts(p, typeName); ok {
				got[typeName] = vals
				found[typeName] = true
			}
		}
		return nil
	})

	for typeName, want := range webhookLabelEnumWant {
		if !found[typeName] {
			t.Fatalf("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01: enum type %q not found in %s — "+
				"renamed or removed?", typeName, webhookPkg)
		}
		vals := got[typeName]
		if len(vals) == 0 {
			t.Fatalf("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01: found 0 consts of type %q in %s",
				typeName, webhookPkg)
		}
		if diff := resultValuesDiff(vals, want); diff != "" {
			t.Fatalf("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01: %q value set drifted from the frozen want-set.\n%s\n"+
				"If intentional, update ALL sync points in the same PR: (1) webhookLabelEnumWant here, "+
				"(2) kernel/webhook/metrics.go enum consts, (3) dashboards/alerts, (4) PR-6 observability docs.",
				typeName, diff)
		}
		if len(vals) != len(want) {
			t.Errorf("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01: type %q has %d consts, want exactly %d",
				typeName, len(vals), len(want))
		}
	}
}

// scanWebhookRecordDeliveryCallsites is the downstream callsite guard: it flags
// any (Metrics).recordDelivery call whose result argument (index 2) is a
// compile-time CONSTANT that is not a bare reference to a declared const. This
// closes the hole the sealed webhookDeliveryResult type cannot — an untyped
// string literal is assignable to the defined type. Allowed: a named const Ident
// (deliverySuccess/…/deliveryBlocked) or any non-constant webhookDeliveryResult
// expression (dispositionResult(...) / statusResult(...) calls). Banned: a string
// literal or a webhookDeliveryResult("x") conversion.
func scanWebhookRecordDeliveryCallsites(p *Pass) []Diagnostic {
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
			if !ok || sel.Sel.Name != "recordDelivery" {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != "recordDelivery" || len(call.Args) < 3 {
				return
			}
			arg := call.Args[2]
			tv, ok := info.Types[arg]
			if !ok || tv.Value == nil {
				return // non-constant (typed var / func call) — allowed
			}
			if id, isIdent := arg.(*ast.Ident); isIdent {
				if _, isConst := info.ObjectOf(id).(*types.Const); isConst {
					return
				}
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(arg.Pos()).Line,
				Message: "recordDelivery result argument is an inline constant — pass a declared " +
					"delivery* const or a typed (non-constant) webhookDeliveryResult, not a string " +
					"literal or webhookDeliveryResult(...) conversion " +
					"(WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 callsite guard)",
			})
		})
	}
	return diags
}

// TestWebhookMetricLabelValuesFrozen01_CallsiteGuard is the production GREEN
// baseline: every recordDelivery call in kernel/webhook passes a declared const
// or a typed (non-constant) value — never an inline literal.
func TestWebhookMetricLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	const webhookPkg = PlatformModulePath + "/kernel/webhook"
	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != webhookPkg {
			return nil
		}
		allDiags = append(allDiags, scanWebhookRecordDeliveryCallsites(p)...)
		return nil
	})
	Report(t, "WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestWebhookMetricLabelValuesFrozen01_NegativeControl proves the value-set
// comparison is non-vacuous: a synthetically drifted set MUST produce a
// non-empty diff, and the correct set MUST produce an empty diff.
func TestWebhookMetricLabelValuesFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	want := webhookLabelEnumWant["webhookDeliveryResult"]

	withExtra := append([]string{}, want...)
	withExtra = append(withExtra, "panic")
	if diff := resultValuesDiff(withExtra, want); diff == "" {
		t.Fatal("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 negative control: a set with an extra 'panic' " +
			"value produced an empty diff — the comparison is vacuous")
	}

	withRenamed := []string{"success", "retry", "server_error", "transport_error", "blocked"}
	if diff := resultValuesDiff(withRenamed, want); diff == "" {
		t.Fatal("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 negative control: 'client_error' renamed to " +
			"'retry' produced an empty diff — the comparison is vacuous")
	}

	if diff := resultValuesDiff(want, want); diff != "" {
		t.Fatalf("WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 negative control: the frozen want-set compared "+
			"against itself produced a non-empty diff — the comparison has a bug: %s", diff)
	}
}

// scanSignatureFailureReasonConversions flags any production conversion
// expression `SignatureFailureReason(x)` (kernel/webhook) — the documented
// blind spot of the recordDelivery-only callsite guard. The receive-side reason
// flows from runtime/webhook's verify() as a typed value and is only ever a
// declared ReasonX const; a conversion would be the bypass that introduces a
// label value outside the frozen set without touching the const declarations.
// The reverse self-check asserts production code contains zero such conversions.
func scanSignatureFailureReasonConversions(p *Pass) []Diagnostic {
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
			if len(call.Args) != 1 {
				return
			}
			var obj types.Object
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				obj = info.ObjectOf(fn)
			case *ast.SelectorExpr:
				obj = info.ObjectOf(fn.Sel)
			default:
				return
			}
			tn, ok := obj.(*types.TypeName)
			if !ok || tn.Name() != "SignatureFailureReason" || tn.Pkg() == nil {
				return
			}
			if tn.Pkg().Path() != PlatformModulePath+"/kernel/webhook" {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(call.Pos()).Line,
				Message: "SignatureFailureReason(...) conversion in production — pass a declared " +
					"ReasonX const (the value-set freeze caps that set), never a conversion that could " +
					"introduce an off-set label value (WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 blind-spot self-check)",
			})
		})
	}
	return diags
}

// TestWebhookMetricLabelValuesFrozen01_NoReasonConversion is the blind-spot
// reverse self-check (ai-robust.md §"工具选定后强制盲区自检"): the recordDelivery
// callsite guard does not cover RecordSignatureFailure (cross-package, exported),
// so a `SignatureFailureReason("x")` conversion would bypass it. This test proves
// that form does not appear in production kernel/webhook or runtime/webhook.
func TestWebhookMetricLabelValuesFrozen01_NoReasonConversion(t *testing.T) {
	t.Parallel()
	const (
		kernelWebhookPkg  = PlatformModulePath + "/kernel/webhook"
		runtimeWebhookPkg = PlatformModulePath + "/runtime/webhook"
	)
	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if p.Pkg.Path() != kernelWebhookPkg && p.Pkg.Path() != runtimeWebhookPkg {
			return nil
		}
		allDiags = append(allDiags, scanSignatureFailureReasonConversions(p)...)
		return nil
	})
	Report(t, "WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestWebhookMetricLabelValuesFrozen01_CallsiteGuard_Fixtures proves the callsite
// guard is non-vacuous: the RED fixture (inline literal) is flagged, the GREEN
// fixture (named const + typed var) is not.
func TestWebhookMetricLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
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
			pattern := "./tools/archtest/testdata/webhook_metric_callsite_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), scanWebhookRecordDeliveryCallsites)
			if len(diags) != c.want {
				t.Fatalf("callsite-guard fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
