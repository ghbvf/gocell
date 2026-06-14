//go:build archtest

// INVARIANT: WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the value sets of the two sealed string-typed
// label enums in kernel/webhook — the `result` label on webhook_deliveries_total
// and the `reason` label on webhook_signature_failures_total — are frozen to
// exactly:
//
//	webhookDeliveryResult  → {success, client_error, server_error, transport_error, blocked, circuit_open}
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
// # AI-robust rating (closed two-direction funnel)
//
// The funnel has three locked dimensions; grade each direction separately
// (ai-robust.md §"Funnel 双向锁评级"):
//
//	Upstream — metric write surface
//	  • OUTSIDE kernel/webhook: Hard. The kwh.Metrics instrument fields are
//	    unexported, so no holder can call `m.Deliveries.With(Labels{...})` — the
//	    only metric-write surface is the record* methods, which take the sealed
//	    typed enums. A populated Metrics is constructible only by RegisterMetrics
//	    (the sole constructor) in-package.
//	  • WITHIN kernel/webhook: Medium. A new in-package function could still call
//	    `m.<field>.With(...)` directly; scanWebhookMetricSoleWriter locks those
//	    callsites to the sanctioned record* methods. This is the Go package-
//	    visibility ceiling (same family as SPAN-SETATTR-HOLDER-SEAL #851 /
//	    HEALTHZ-HOLDER-SEAL #893).
//
//	Downstream — label value reaching a record* method
//	  • Medium. scanEnumLabelCallsite resolves the arg to a declared const of the
//	    sealed enum TYPE (object identity via the method's own signature, so it is
//	    rename-proof and cross-package). It covers recordDelivery (delivery) AND
//	    RecordSignatureFailure (reason) — closing the prior gaps where (a) the
//	    delivery guard accepted ANY *types.Const, not just delivery* consts (F2),
//	    and (b) the reason side had no callsite guard at all, so a bare string
//	    literal implicitly converted to SignatureFailureReason (F2′). The residual
//	    is the trusted "typed non-constant value" path (classifier output), which
//	    the conversion bans make safe (a value cannot be minted off-set without a
//	    conversion). Hard ceiling = the same package-scoped string type as above.
//
//	Value-set membership
//	  • Medium. go/types const enumeration by sealed TYPE identity vs an
//	    independent hardcoded want-set (both directions: nothing missing, nothing
//	    extra). Hard upgrade path = enroll both value sets into the metricschema
//	    golden so a 6th value is byte-locked at codegen time. Shared with saga/
//	    reconcile at gh #1416 — do NOT open a duplicate tracking issue.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Enumeration is by enum TYPE identity (types.Identical), not by name prefix:
//     a const renamed off the Reason*/delivery* convention but still typed as the
//     enum is still counted, and a new const of the enum type is still caught.
//
//   - Conversions are the only way to mint an off-set value of either enum.
//     scanWebhookDeliveryResultConversions bans webhookDeliveryResult(...) in
//     kernel/webhook (the type is unexported, so it cannot be named elsewhere) and
//     scanSignatureFailureReasonConversions bans SignatureFailureReason(...) across
//     kernel/webhook + runtime/webhook (it is exported). Together with the callsite
//     guards (which reject untyped-const and literal forms reaching the record*
//     methods) and the unexported-field upstream seal, all three mint/bypass
//     vectors — literal, untyped const, conversion-laundered-through-var/func — are
//     closed.
//
//   - The sole-writer lock and the delivery conversion ban run only on the
//     kernel/webhook Pass; the RecordSignatureFailure callsite guard additionally
//     runs on runtime/webhook (its only cross-package caller).
//
// # Reverse self-check (non-vacuous proof)
//
// TestWebhookMetricLabelValuesFrozen01_NegativeControl demonstrates a synthetic
// extra/renamed value is detected. The CallsiteGuard_Fixtures test proves every
// guard is non-vacuous against real-source fixtures: red_literal / red_untyped_const
// (recordDelivery guard), red_reason_literal (RecordSignatureFailure guard), and
// red_dr_conversion (delivery conversion ban) are each flagged, while green passes.
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
		"success", "client_error", "server_error", "transport_error", "blocked", "circuit_open",
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

	const webhookPkg = PlatformFrameworkModulePath + "/kernel/webhook"
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

// constObjectOf resolves the declared object an argument expression refers to
// when it is a bare identifier (e.g. deliverySuccess) or a package-qualified
// selector (e.g. kwh.ReasonBadSignature). Any other expression form returns nil.
func constObjectOf(info *types.Info, arg ast.Expr) types.Object {
	switch e := arg.(type) {
	case *ast.Ident:
		return info.ObjectOf(e)
	case *ast.SelectorExpr:
		return info.ObjectOf(e.Sel)
	}
	return nil
}

// scanEnumLabelCallsite is the downstream callsite guard for a sealed-string-typed
// metric label argument: it flags any call to method methodName whose argument at
// argIdx is a compile-time CONSTANT that is NOT a reference to a declared const of
// the sealed enum type. The enum type is taken from the method's own signature
// (param argIdx), so the check is rename-proof and works cross-package (a runtime/
// webhook caller of the exported RecordSignatureFailure resolves to the kernel/
// webhook SignatureFailureReason consts).
//
// Allowed:
//   - a non-constant value of the enum type (the trusted classifier output:
//     statusResult(...) / dispositionResult(...) for delivery; verify()'s typed
//     return for the reason side), AND
//   - a declared const whose TYPE is identical to the enum type
//     (deliverySuccess/… or ReasonBadSignature/…).
//
// Banned — every other constant form:
//   - an inline string literal ("rogue"),
//   - an untyped string const ident (const x = "rogue"; …(x)) whose object type is
//     untyped string, NOT the enum type (this is the F2 hole the prior guard let
//     through — it accepted ANY *types.Const),
//   - a `webhookDeliveryResult("x")` / `SignatureFailureReason("x")` conversion
//     (also independently banned by the conversion scanners).
//
// Matching is by method NAME (+ arg arity); the production tests scope the scan to
// the webhook packages, and the fixtures declare a local Metrics with the same
// method shape.
func scanEnumLabelCallsite(p *Pass, methodName string, argIdx int) []Diagnostic {
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
			if !ok || sel.Sel.Name != methodName {
				return
			}
			fn, ok := ResolveMethodCall(info, sel)
			if !ok || fn.Name() != methodName || len(call.Args) <= argIdx {
				return
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || sig.Params().Len() <= argIdx {
				return
			}
			enumType := sig.Params().At(argIdx).Type()
			arg := call.Args[argIdx]
			tv, ok := info.Types[arg]
			if !ok || tv.Value == nil {
				return // non-constant typed value (trusted classifier output) — allowed
			}
			if obj := constObjectOf(info, arg); obj != nil {
				if _, isConst := obj.(*types.Const); isConst && types.Identical(obj.Type(), enumType) {
					return // a declared const OF the sealed enum type — allowed
				}
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(arg.Pos()).Line,
				Message: methodName + " label argument is a constant that is not a declared " +
					enumType.String() + " const — pass a declared enum const or a typed " +
					"(non-constant) value, never a string literal, an untyped const, or a " +
					enumType.String() + "(...) conversion " +
					"(WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 callsite guard)",
			})
		})
	}
	return diags
}

// scanWebhookRecordDeliveryCallsites guards the delivery-result enum on
// (Metrics).recordDelivery (arg index 2).
func scanWebhookRecordDeliveryCallsites(p *Pass) []Diagnostic {
	return scanEnumLabelCallsite(p, "recordDelivery", 2)
}

// scanWebhookRecordSignatureFailureCallsites guards the reason enum on the
// exported (Metrics).RecordSignatureFailure (arg index 2). Exported + called
// cross-package, so the production test scopes it to kernel/webhook + runtime/
// webhook. Closes the F2′ hole: a bare string literal implicitly converts to
// SignatureFailureReason and the conversion-ban scanner (which matches only an
// explicit SignatureFailureReason(...) CallExpr) cannot see it.
func scanWebhookRecordSignatureFailureCallsites(p *Pass) []Diagnostic {
	return scanEnumLabelCallsite(p, "RecordSignatureFailure", 2)
}

// scanWebhookDeliveryResultConversions flags any `webhookDeliveryResult(...)`
// conversion in the scanned package — the delivery-side mirror of
// scanSignatureFailureReasonConversions. webhookDeliveryResult is unexported, so
// the enum type is resolved from the scanned package's own scope (the production
// kernel/webhook package and each red fixture both declare it locally); a
// conversion is the only way to mint a value outside the declared const set, so
// banning it makes the "non-constant typed value" allowance of the callsite guard
// safe (the trusted classifiers return declared consts, never conversions).
func scanWebhookDeliveryResultConversions(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	tn, ok := p.Pkg.Scope().Lookup("webhookDeliveryResult").(*types.TypeName)
	if !ok {
		return nil
	}
	enumType := tn.Type()
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
			obj := constObjectOf(info, call.Fun)
			tnObj, ok := obj.(*types.TypeName)
			if !ok || !types.Identical(tnObj.Type(), enumType) {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(call.Pos()).Line,
				Message: "webhookDeliveryResult(...) conversion in production — pass a declared " +
					"delivery* const (the value-set freeze caps that set), never a conversion that " +
					"could introduce an off-set label value " +
					"(WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 blind-spot self-check)",
			})
		})
	}
	return diags
}

// scanWebhookMetricSoleWriter is the within-package upstream lock: with the
// kwh.Metrics instrument fields unexported, the only metric-write surface OUTSIDE
// kernel/webhook is the record* methods (Go visibility — Hard). WITHIN the package
// a new function could still call `m.<field>.With(...)` directly and bypass the
// record* funnel; this scanner asserts every `<Metrics>.<instrument>.With(...)`
// call sits inside one of the sanctioned writer methods. Medium (package-internal
// allowlist; the Go-visibility ceiling is the same as the #851/#893 holder-seal
// family). Hard upgrade path = metricschema golden byte-lock, gh #1416.
func scanWebhookMetricSoleWriter(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	tn, ok := p.Pkg.Scope().Lookup("Metrics").(*types.TypeName)
	if !ok {
		return nil
	}
	metricsType := tn.Type()
	instrumentFields := map[string]bool{
		"deliveries": true, "deliveryDuration": true,
		"signatureFailures": true, "idempotencyHits": true,
	}
	sanctioned := map[string]bool{
		"recordDelivery": true, "observeDeliveryDuration": true,
		"RecordSignatureFailure": true, "RecordIdempotencyHit": true, "preflight": true,
	}
	isInstrumentWith := func(call *ast.CallExpr) bool {
		withSel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || withSel.Sel.Name != "With" {
			return false
		}
		fieldSel, ok := withSel.X.(*ast.SelectorExpr)
		if !ok || !instrumentFields[fieldSel.Sel.Name] {
			return false
		}
		recvType := info.TypeOf(fieldSel.X)
		if recvType == nil {
			return false
		}
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		return types.Identical(recvType, metricsType)
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Body == nil {
				return
			}
			EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
				if !isInstrumentWith(call) || sanctioned[fn.Name.Name] {
					return
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: "Metrics instrument .With(...) called outside the sanctioned record* " +
						"writers (recordDelivery/observeDeliveryDuration/RecordSignatureFailure/" +
						"RecordIdempotencyHit/preflight) in " + fn.Name.Name + " — the unexported " +
						"instrument fields must only be written through the record* funnel " +
						"(WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01 upstream sole-writer lock)",
				})
			})
		})
	}
	return diags
}

// TestWebhookMetricLabelValuesFrozen01_CallsiteGuard is the production GREEN
// baseline for the full downstream + upstream funnel:
//   - recordDelivery (kernel/webhook): every result arg is a declared delivery*
//     const or a typed non-constant (statusResult/dispositionResult output).
//   - RecordSignatureFailure (kernel/webhook + runtime/webhook): every reason arg
//     is a declared Reason* const or the typed verify() return — never a literal.
//   - webhookDeliveryResult(...) conversions (kernel/webhook): zero.
//   - Metrics instrument .With(...) sole-writer lock (kernel/webhook): every
//     instrument write sits inside a sanctioned record* method.
func TestWebhookMetricLabelValuesFrozen01_CallsiteGuard(t *testing.T) {
	t.Parallel()

	const (
		kernelWebhookPkg  = PlatformFrameworkModulePath + "/kernel/webhook"
		runtimeWebhookPkg = PlatformFrameworkModulePath + "/runtime/webhook"
	)
	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		switch p.Pkg.Path() {
		case kernelWebhookPkg:
			allDiags = append(allDiags, scanWebhookRecordDeliveryCallsites(p)...)
			allDiags = append(allDiags, scanWebhookRecordSignatureFailureCallsites(p)...)
			allDiags = append(allDiags, scanWebhookDeliveryResultConversions(p)...)
			allDiags = append(allDiags, scanWebhookMetricSoleWriter(p)...)
		case runtimeWebhookPkg:
			// RecordSignatureFailure is exported and called from the receiver here.
			allDiags = append(allDiags, scanWebhookRecordSignatureFailureCallsites(p)...)
		}
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
			if tn.Pkg().Path() != PlatformFrameworkModulePath+"/kernel/webhook" {
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
		kernelWebhookPkg  = PlatformFrameworkModulePath + "/kernel/webhook"
		runtimeWebhookPkg = PlatformFrameworkModulePath + "/runtime/webhook"
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

// TestWebhookMetricLabelValuesFrozen01_CallsiteGuard_Fixtures proves each guard is
// non-vacuous against real-source RED/GREEN fixtures:
//   - green               → recordDelivery guard: declared const + typed non-const
//   - classifier call are all allowed (0).
//   - red_literal         → recordDelivery guard: inline string literal flagged (1).
//   - red_untyped_const   → recordDelivery guard: untyped string const (the F2 hole
//     the prior "any *types.Const" check let through) flagged (1).
//   - red_reason_literal  → RecordSignatureFailure guard: inline literal reason (the
//     F2′ implicit-conversion hole the reason side had no callsite guard for) (1).
//   - red_dr_conversion   → delivery conversion ban: webhookDeliveryResult("x")
//     conversion flagged (1).
func TestWebhookMetricLabelValuesFrozen01_CallsiteGuard_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		scan func(*Pass) []Diagnostic
		want int
	}{
		{"green", scanWebhookRecordDeliveryCallsites, 0},
		{"red_literal", scanWebhookRecordDeliveryCallsites, 1},
		{"red_untyped_const", scanWebhookRecordDeliveryCallsites, 1},
		{"red_reason_literal", scanWebhookRecordSignatureFailureCallsites, 1},
		{"red_dr_conversion", scanWebhookDeliveryResultConversions, 1},
	}
	for _, c := range cases {
		c := c
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/webhook_metric_callsite_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), c.scan)
			if len(diags) != c.want {
				t.Fatalf("callsite-guard fixture %s: want %d diagnostic(s), got %d: %v",
					c.dir, c.want, len(diags), diags)
			}
		})
	}
}
