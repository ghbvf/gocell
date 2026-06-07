// INVARIANT: MQTT-METRIC-LABEL-VALUES-FROZEN-01
//
// This file owns ONE invariant: the observable metric surface of
// adapters/mqtt is frozen against silent drift. EPIC #1138 spec AC-8 (#1429)
// drifted from its literal metric names — packet_too_large_total folded into a
// reason label, subscribe_inflight unimplemented — with no machine guard to
// catch the next drift. There are TWO frozen objects, each taking the minimal
// dimension set it needs (no gold-plating):
//
//	(a) Metric NAME set — enumeration freeze only. The 11 mqtt_* names are written
//	    exactly once each, in a metrics.{Counter,Histogram,Gauge}Opts composite
//	    literal Name: field; there is no callsite that takes a metric name, so a
//	    name has no funnel to lock — a name-vs-golden enumeration is sufficient.
//
//	(b) Reason LABEL value set — full funnel. The three sealed string enums
//	    (PublishFailureReason / ConsumeFailureReason / SubscribeFailureReason)
//	    reach a {reason} metric label through a Record* method arg, so the value
//	    set is closed by four locked dimensions (each closes one bypass vector):
//	    value-set freeze + downstream callsite guard + conversion ban + upstream
//	    sole-writer lock.
//
// Runtime drift (a renamed metric, a 7th PublishFailureReason, a bare literal
// reaching a {reason} label) would break dashboards / alerting / the ADR-048
// §2.7 closed-set claim without any compile error. This archtest also machine-
// enforces ADR-048 §2.7's previously-prose "reason 闭集（10 个）" claim.
//
// # AI-robust rating (per ai-robust.md §"Funnel 双向锁评级")
//
//	Metric NAME set (a): Medium. go/types enumeration of every Opts literal's
//	  Name: field vs an independent hardcoded want-set (anti-tautology, both
//	  directions). Hard upgrade path = enroll the names into the metricschema
//	  golden so a rename is byte-locked at codegen time — shared with saga /
//	  reconcile / webhook / idempotency at gh #1416, do NOT open a duplicate.
//
//	Reason value-set (b):
//	  Upstream — metric write surface:
//	    • OUTSIDE adapters/mqtt: Hard. The provider*Collector instrument fields
//	      are unexported, so no holder can call `c.<field>.With(Labels{...})` —
//	      the only metric-write surface is the Record*/AdjustInflight methods,
//	      which take the sealed typed enums (reason metrics) or no label at all
//	      (AdjustInflight: the cell label is construction-bound and cannot be
//	      forged). A populated provider*Collector is constructible only by the
//	      NewProvider*Collector constructors in-package.
//	    • WITHIN adapters/mqtt: Medium. A new in-package function could still call
//	      `c.<field>.With(...)` directly; scanMqttMetricSoleWriter locks those
//	      callsites to the sanctioned Record*/AdjustInflight methods. This is the
//	      Go package-visibility ceiling (same family as SPAN-SETATTR-HOLDER-SEAL
//	      #851 / HEALTHZ-HOLDER-SEAL #893).
//	  Downstream — label value reaching a Record* method: Medium.
//	    scanEnumLabelCallsite resolves the arg to a declared const of the sealed
//	    enum TYPE (object identity via the method's own signature, rename-proof
//	    and cross-package). The residual trusted path is the "typed non-constant
//	    value" (classifier output), which the conversion ban makes safe.
//	  Value-set membership: Medium. go/types const enumeration by sealed TYPE
//	    identity vs an independent hardcoded want-set (both directions). Hard
//	    upgrade path = metricschema golden, shared gh #1416.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Enumeration is by enum TYPE identity (types.Identical), not name prefix: a
//     const renamed off the *Reason/reason* convention but still typed as the
//     enum is still counted; a new const of the enum type is still caught.
//   - Conversions are the only way to mint an off-set value of a sealed enum.
//     scanMqttReasonConversions bans the three XReason(...) conversions in
//     adapters/mqtt; together with the callsite guard (rejects untyped-const and
//     literal forms) and the unexported-field upstream seal, the literal /
//     untyped-const / conversion-laundered vectors are all closed.
//   - The metric-name scanner reads the Name: field of every Opts composite
//     literal (inline — connection/publisher constructors — AND package-level
//     var — subscriber). Both are *ast.CompositeLit, so one walk covers both
//     declaration forms.
//   - Record* methods are invoked only within adapters/mqtt (the adapter owns its
//     metric emission); there is no cross-package emission caller, so the
//     conversion ban + callsite guard scoped to adapters/mqtt close the surface.
//
// # Reverse self-check (non-vacuous proof)
//
// TestMqttMetricFrozen01_NegativeControl proves the value-set comparison catches
// a synthetic extra / renamed value (name set AND reason set). The _Fixtures
// test proves each downstream/upstream guard is non-vacuous against real-source
// RED/GREEN fixtures: green (0), red_literal (callsite guard, 1), red_conversion
// (conversion ban, 1), red_direct_write (sole-writer lock, 1).
package archtest

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"testing"
)

// mqttPkgPath ("…/adapters/mqtt") is declared in mqtt_funnel.go and reused here.

// mqttMetricNameWant is the frozen set of every metric name registered by
// adapters/mqtt. Updating it requires a simultaneous update to: (1) this slice,
// (2) the metrics.*Opts Name: fields in adapters/mqtt/metrics.go, (3) dashboards
// / alerts referencing the renamed series, and (4) ADR-048 §Amendment 2026-06-07.
var mqttMetricNameWant = []string{
	"mqtt_reconnect_total",
	"mqtt_subscribe_failed_total",
	"mqtt_publish_total",
	"mqtt_publish_failed_total",
	"mqtt_publish_ack_duration_seconds",
	"mqtt_consume_total",
	"mqtt_consume_failed_total",
	"mqtt_dlx_total",
	"mqtt_dlx_failed_total",
	"mqtt_consume_duration_seconds",
	"mqtt_consume_inflight",
}

// mqttReasonEnumWant is the frozen membership of the three sealed reason enums,
// keyed by enum type name. Updating any entry requires a simultaneous update to:
// (1) this map, (2) the const block in adapters/mqtt/metrics.go, (3) the metric
// Help "reason ∈ {…}" closed-set text, (4) dashboards/alerts, (5) ADR-048 §2.7.
var mqttReasonEnumWant = map[string][]string{
	"PublishFailureReason": {
		"payload_too_large", "closed", "puback_timeout", "context_canceled",
		"publish_error", "topic_outside_namespace", "rate_limited",
		"not_authorized", "payload_format_invalid", "rejected",
	},
	"ConsumeFailureReason": {
		"unmarshal", "reject", "requeue", "commit_failed", "ack_failed",
		"unknown_disposition",
	},
	"SubscribeFailureReason": {
		"suback_reject", "transport",
	},
}

// mqttMetricsOptsTypes is the set of kernel/observability/metrics Opts struct
// names whose Name: field declares a metric name.
var mqttMetricsOptsTypes = map[string]bool{
	"CounterOpts": true, "HistogramOpts": true, "GaugeOpts": true,
}

// collectMqttMetricNames enumerates the Name: field string value of every
// metrics.{Counter,Histogram,Gauge}Opts composite literal in p (production
// files only). Covers both inline literals (connection/publisher constructors)
// and package-level var literals (subscriber) — both are *ast.CompositeLit.
func collectMqttMetricNames(p *Pass) []string {
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	const metricsPkg = PlatformModulePath + "/kernel/observability/metrics"
	var names []string
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
			named, ok := info.TypeOf(lit).(*types.Named)
			if !ok {
				return
			}
			obj := named.Obj()
			if obj.Pkg() == nil || obj.Pkg().Path() != metricsPkg || !mqttMetricsOptsTypes[obj.Name()] {
				return
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Name" {
					continue
				}
				if tv, ok := info.Types[kv.Value]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
					names = append(names, constant.StringVal(tv.Value))
				}
			}
		})
	}
	return names
}

// TestMqttMetricNamesFrozen01 freezes the metric NAME set registered by
// adapters/mqtt against the independent hardcoded want-set, both directions.
func TestMqttMetricNamesFrozen01(t *testing.T) {
	t.Parallel()
	var got []string
	found := false
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
			return nil
		}
		got = collectMqttMetricNames(p)
		found = true
		return nil
	})
	if !found {
		t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: package %s not found in production scan", mqttPkgPath)
	}
	if len(got) == 0 {
		t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: found 0 metric Opts Name: literals in %s", mqttPkgPath)
	}
	if diff := resultValuesDiff(got, mqttMetricNameWant); diff != "" {
		t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: metric NAME set drifted from the frozen want-set.\n%s\n"+
			"If intentional, update ALL sync points in the same PR: (1) mqttMetricNameWant here, "+
			"(2) adapters/mqtt/metrics.go *Opts Name: fields, (3) dashboards/alerts, (4) ADR-048 §Amendment 2026-06-07.",
			diff)
	}
	if len(got) != len(mqttMetricNameWant) {
		t.Errorf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: %d metric names registered, want exactly %d",
			len(got), len(mqttMetricNameWant))
	}
}

// TestMqttMetricReasonValuesFrozen01 freezes the three sealed reason enum VALUE
// sets in adapters/mqtt against the independent hardcoded want-sets, both
// directions: nothing missing, nothing extra. (Reuses the shared generic
// collectWebhookEnumConsts — it looks a type up by name in the pass scope and
// enumerates its string consts; not webhook-specific.)
func TestMqttMetricReasonValuesFrozen01(t *testing.T) {
	t.Parallel()
	got := map[string][]string{}
	found := map[string]bool{}
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
			return nil
		}
		for typeName := range mqttReasonEnumWant {
			if vals, ok := collectWebhookEnumConsts(p, typeName); ok {
				got[typeName] = vals
				found[typeName] = true
			}
		}
		return nil
	})
	for typeName, want := range mqttReasonEnumWant {
		if !found[typeName] {
			t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: enum type %q not found in %s — renamed or removed?",
				typeName, mqttPkgPath)
		}
		vals := got[typeName]
		if len(vals) == 0 {
			t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: found 0 consts of type %q in %s", typeName, mqttPkgPath)
		}
		if diff := resultValuesDiff(vals, want); diff != "" {
			t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: %q value set drifted from the frozen want-set.\n%s\n"+
				"If intentional, update ALL sync points in the same PR: (1) mqttReasonEnumWant here, "+
				"(2) adapters/mqtt/metrics.go enum consts, (3) the metric Help closed-set text, "+
				"(4) dashboards/alerts, (5) ADR-048 §2.7.", typeName, diff)
		}
		if len(vals) != len(want) {
			t.Errorf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: type %q has %d consts, want exactly %d",
				typeName, len(vals), len(want))
		}
	}
}

// mqttReasonRecordMethods are the Record* methods that take a sealed reason enum
// at arg index 1 (ctx is index 0).
var mqttReasonRecordMethods = []string{
	"RecordPublishFailure",
	"RecordSubscribeFailure",
	"RecordConsumeFailure",
	"RecordDeadLetter",
	"RecordDeadLetterFailure",
}

// scanMqttReasonCallsites is the downstream callsite guard: every reason arg to a
// Record* method must be a declared const of the enum type (or a trusted typed
// non-constant value), never a literal / untyped const / conversion.
func scanMqttReasonCallsites(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, m := range mqttReasonRecordMethods {
		diags = append(diags, scanEnumLabelCallsite(p, m, 1)...)
	}
	return diags
}

// scanMqttReasonConversions flags any XReason(...) conversion of one of the three
// sealed reason enums in the scanned package's production code — the conversion is
// the only way to mint an off-set value, so banning it makes the callsite guard's
// "typed non-constant value" allowance safe. The enum types are resolved from the
// scanned package's own scope (production adapters/mqtt OR a red fixture), so the
// scanner is non-vacuous against both.
func scanMqttReasonConversions(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	var bannedTypes []types.Type
	for typeName := range mqttReasonEnumWant {
		if tn, ok := p.Pkg.Scope().Lookup(typeName).(*types.TypeName); ok {
			bannedTypes = append(bannedTypes, tn.Type())
		}
	}
	if len(bannedTypes) == 0 {
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
			tn, ok := constObjectOf(info, call.Fun).(*types.TypeName)
			if !ok {
				return
			}
			for _, bt := range bannedTypes {
				if types.Identical(tn.Type(), bt) {
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: tn.Name() + "(...) conversion in production — pass a declared reason const " +
							"(the value-set freeze caps that set), never a conversion that could introduce an " +
							"off-set label value (MQTT-METRIC-LABEL-VALUES-FROZEN-01 blind-spot self-check)",
					})
					return
				}
			}
		})
	}
	return diags
}

// mqttCollectorStructNames are the three provider collector structs holding the
// unexported metric instrument fields.
var mqttCollectorStructNames = []string{
	"providerConnectionCollector",
	"providerPublisherCollector",
	"providerSubscriberCollector",
}

// mqttInstrumentFields are the unexported instrument fields of the three provider
// collectors (cellID excluded — it is not an instrument).
var mqttInstrumentFields = map[string]bool{
	"reconnect": true, "subscribeFail": true,
	"publishTotal": true, "publishFailed": true, "ackDuration": true,
	"consumeTotal": true, "consumeFailed": true, "dlxTotal": true,
	"dlxFailed": true, "consumeDur": true, "consumeInflight": true,
}

// mqttSanctionedWriters are the methods allowed to write an instrument field.
var mqttSanctionedWriters = map[string]bool{
	"RecordReconnect": true, "RecordSubscribeFailure": true,
	"RecordPublishSuccess": true, "RecordPublishFailure": true,
	"RecordConsumeSuccess": true, "RecordConsumeFailure": true,
	"RecordDeadLetter": true, "RecordDeadLetterFailure": true,
	"AdjustInflight": true,
}

// scanMqttMetricSoleWriter is the within-package upstream lock: every
// `c.<instrument>.With(...)` call where c is one of the provider collector types
// must sit inside a sanctioned Record*/AdjustInflight method. The instrument
// fields are unexported (Go visibility — Hard outside the package); within the
// package this scanner is the Medium backstop.
func scanMqttMetricSoleWriter(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	var collectorTypes []types.Type
	for _, n := range mqttCollectorStructNames {
		if tn, ok := p.Pkg.Scope().Lookup(n).(*types.TypeName); ok {
			collectorTypes = append(collectorTypes, tn.Type())
		}
	}
	if len(collectorTypes) == 0 {
		return nil
	}
	isInstrumentWith := func(call *ast.CallExpr) bool {
		withSel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || withSel.Sel.Name != "With" {
			return false
		}
		fieldSel, ok := withSel.X.(*ast.SelectorExpr)
		if !ok || !mqttInstrumentFields[fieldSel.Sel.Name] {
			return false
		}
		recvType := info.TypeOf(fieldSel.X)
		if recvType == nil {
			return false
		}
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		for _, ct := range collectorTypes {
			if types.Identical(recvType, ct) {
				return true
			}
		}
		return false
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Body == nil || mqttSanctionedWriters[fn.Name.Name] {
				return
			}
			EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
				if !isInstrumentWith(call) {
					return
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: "provider*Collector instrument .With(...) called outside the sanctioned " +
						"Record*/AdjustInflight writers in " + fn.Name.Name + " — the unexported instrument " +
						"fields must only be written through the metric funnel " +
						"(MQTT-METRIC-LABEL-VALUES-FROZEN-01 upstream sole-writer lock)",
				})
			})
		})
	}
	return diags
}

// TestMqttMetricFunnel01 is the production GREEN baseline for the full reason
// funnel: callsite guard + conversion ban + sole-writer lock all report zero on
// adapters/mqtt.
func TestMqttMetricFunnel01(t *testing.T) {
	t.Parallel()
	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
			return nil
		}
		allDiags = append(allDiags, scanMqttReasonCallsites(p)...)
		allDiags = append(allDiags, scanMqttReasonConversions(p)...)
		allDiags = append(allDiags, scanMqttMetricSoleWriter(p)...)
		return nil
	})
	Report(t, "MQTT-METRIC-LABEL-VALUES-FROZEN-01", allDiags)
}

// TestMqttMetricFrozen01_NegativeControl proves the value-set comparison is
// non-vacuous for BOTH frozen objects: a synthetically drifted set MUST produce a
// non-empty diff, and the correct set MUST produce an empty diff.
func TestMqttMetricFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()
	// metric name set
	withExtraName := append(append([]string{}, mqttMetricNameWant...), "mqtt_rogue_total")
	if diff := resultValuesDiff(withExtraName, mqttMetricNameWant); diff == "" {
		t.Fatal("negative control: extra metric name produced an empty diff — comparison is vacuous")
	}
	if diff := resultValuesDiff(mqttMetricNameWant, mqttMetricNameWant); diff != "" {
		t.Fatalf("negative control: name want-set vs itself produced a non-empty diff — bug: %s", diff)
	}
	// reason value set
	want := mqttReasonEnumWant["PublishFailureReason"]
	withRenamed := append([]string{"panic"}, want[1:]...)
	if diff := resultValuesDiff(withRenamed, want); diff == "" {
		t.Fatal("negative control: renamed reason value produced an empty diff — comparison is vacuous")
	}
	if diff := resultValuesDiff(want, want); diff != "" {
		t.Fatalf("negative control: reason want-set vs itself produced a non-empty diff — bug: %s", diff)
	}
}

// TestMqttMetricFunnel01_Fixtures proves each downstream/upstream guard is
// non-vacuous against real-source RED/GREEN fixtures:
//   - green            → all three guards: declared-const callsite, no conversion,
//     instrument .With only in sanctioned writers (0).
//   - red_literal      → callsite guard: inline string literal reason flagged (1).
//   - red_conversion   → conversion ban: ConsumeFailureReason("x") conversion (1).
//   - red_direct_write → sole-writer lock: instrument .With in a non-sanctioned
//     function flagged (1).
func TestMqttMetricFunnel01_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dir  string
		scan func(*Pass) []Diagnostic
		want int
	}{
		{"green_callsite", "green", scanMqttReasonCallsites, 0},
		{"green_conversion", "green", scanMqttReasonConversions, 0},
		{"green_sole_writer", "green", scanMqttMetricSoleWriter, 0},
		{"red_literal", "red_literal", scanMqttReasonCallsites, 1},
		{"red_conversion", "red_conversion", scanMqttReasonConversions, 1},
		{"red_direct_write", "red_direct_write", scanMqttMetricSoleWriter, 1},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/mqtt_metric_fixtures/" + c.dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), c.scan)
			if len(diags) != c.want {
				t.Fatalf("fixture %s: want %d diagnostic(s), got %d: %v", c.dir, c.want, len(diags), diags)
			}
		})
	}
}
