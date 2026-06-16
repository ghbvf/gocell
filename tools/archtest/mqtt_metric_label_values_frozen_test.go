//go:build archtest

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
//	    set is closed by these locked dimensions (each closes one bypass vector):
//	    value-set freeze + downstream callsite guard + explicit-conversion ban +
//	    reason-constant provenance backstop (return/var/assign/arg implicit
//	    conversion) + upstream sole-writer lock (receiver-identity) + a go/types-
//	    derived instrument-field inventory cross-check.
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
//	    WITHIN adapters/mqtt the sanctioned-writer allowlist keys on RECEIVER TYPE
//	    identity + method name (scanMqttMetricSoleWriter, F2) — a free function or a
//	    method on some other receiver named AdjustInflight/RecordPublishFailure is
//	    NOT sanctioned. The instrument-field set itself is cross-checked against the
//	    go/types-derived inventory (TestMqttMetricInstrumentInventory01) so a new
//	    metrics.*Vec field cannot silently escape the scanner's coverage.
//	  Downstream — label value reaching a Record* method: Medium.
//	    scanEnumLabelCallsite resolves the arg to a declared const of the sealed
//	    enum TYPE (object identity via the method's own signature, rename-proof
//	    and cross-package). The residual "typed non-constant value" (classifier
//	    output) is made safe by BOTH the explicit-conversion ban AND the reason-
//	    constant provenance backstop (scanMqttReasonOffsetConstants, F1), which
//	    flags any off-set constant of a reason enum type at a return / var / assign /
//	    arg — so a classifier can only return declared consts (or the "" sentinel),
//	    never an implicitly-converted literal.
//	  Value-set membership: Medium. go/types const enumeration by sealed TYPE
//	    identity vs an independent hardcoded want-set (both directions). Hard
//	    upgrade path = metricschema golden, shared gh #1416.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Enumeration is by enum TYPE identity (types.Identical), not name prefix: a
//     const renamed off the *Reason/reason* convention but still typed as the
//     enum is still counted; a new const of the enum type is still caught.
//   - Minting an off-set reason value requires a constant string flowing into the
//     enum type. scanMqttReasonOffsetConstants flags EVERY off-set constant of a
//     reason enum (return / var / assignment / arg / composite field — all the
//     implicit-conversion sites), exempting only the const declarations that DEFINE
//     the frozen set and the "" zero-value sentinel; scanMqttReasonConversions
//     additionally bans explicit XReason(nonConst) conversions. Residual (NOT
//     closed): a non-constant value (param / var / classifier call) carrying an
//     off-set value sourced via runtime data flow — the permanent Go ceiling shared
//     with #1282 / #851 / #893; full closure needs data-flow provenance.
//   - The metric-name scanner reads the Name: field of every Opts composite
//     literal (inline — connection/publisher constructors — AND package-level
//     var — subscriber). Both are *ast.CompositeLit, so one walk covers both
//     declaration forms.
//   - Record* methods are invoked only within adapters/mqtt (the adapter owns its
//     metric emission); there is no cross-package emission caller, so the
//     provenance backstop + conversion ban + callsite guard scoped to adapters/mqtt
//     close the surface.
//
// # Reverse self-check (non-vacuous proof)
//
// TestMqttMetricFrozen01_NegativeControl proves the value-set comparison catches
// a synthetic extra / renamed value (name set AND reason set). The _Fixtures
// test proves each guard is non-vacuous against real-source RED/GREEN fixtures:
// green (0), red_literal (callsite guard, 1), red_conversion (conversion ban, 1),
// red_classifier_literal + red_typed_var (provenance backstop, 1 each),
// red_direct_write + red_wrong_writer (sole-writer lock, 1 each).
// TestMqttMetricInstrumentInventory01 additionally cross-checks the instrument-
// field inventory against go/types so the sole-writer field set cannot go stale.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/token"
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
	const metricsPkg = PlatformFrameworkModulePath + "/kernel/observability/metrics"
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
			EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Name" {
					return
				}
				if tv, ok := info.Types[kv.Value]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
					names = append(names, constant.StringVal(tv.Value))
				}
			})
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

// scanMqttReasonOffsetConstants is the provenance backstop (F1, codex review): a
// reason enum is a defined-string type, so an untyped string constant is
// implicitly convertible to it at any context — return, var/const init,
// assignment, call arg, composite field. The explicit-conversion ban only sees
// XReason(...) CallExprs; the callsite guard only sees Record* args. This scanner
// closes ALL implicit-constant vectors in one pass: every CONSTANT expression
// whose type is a reason enum must be (a) a reference to a declared enum const,
// (b) the empty-string zero value (the no-failure sentinel returned by
// sendSubscribe), or (c) the value expr of the const declaration that DEFINES a
// frozen const (exempt). Any other constant — e.g. `func classify() Reason {
// return "rogue" }` or `var x Reason = "rogue"` — is an off-set mint and flagged.
//
// Residual (documented blind spot, NOT closed): a NON-constant reason value
// (param / var / classifier call) reaching a sink. That is the trusted-typed-
// value path the callsite guard also permits; fully locking it needs data-flow
// provenance — the permanent Go ceiling shared with #1282 / #851 / #893.
func scanMqttReasonOffsetConstants(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	var enumTypes []types.Type
	for typeName := range mqttReasonEnumWant {
		if tn, ok := p.Pkg.Scope().Lookup(typeName).(*types.TypeName); ok {
			enumTypes = append(enumTypes, tn.Type())
		}
	}
	if len(enumTypes) == 0 {
		return nil
	}
	isEnum := func(t types.Type) bool {
		for _, e := range enumTypes {
			if types.Identical(t, e) {
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
		// Exempt the value exprs of const specs that DEFINE enum consts — those are
		// the frozen-value declaration sites, not off-set mints.
		exempt := map[token.Pos]bool{}
		EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if gd.Tok != token.CONST {
				return
			}
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for _, v := range vs.Values {
					if tv, ok := info.Types[v]; ok && tv.Value != nil && isEnum(tv.Type) {
						exempt[v.Pos()] = true
					}
				}
			})
		})
		seen := map[token.Pos]bool{}
		checkExpr := func(expr ast.Expr) {
			if seen[expr.Pos()] {
				return
			}
			seen[expr.Pos()] = true
			tv, ok := info.Types[expr]
			if !ok || tv.Value == nil || !isEnum(tv.Type) {
				return // not a constant of a reason enum type
			}
			if exempt[expr.Pos()] {
				return // the defining const spec value
			}
			if c, ok := constObjectOf(info, expr).(*types.Const); ok && types.Identical(c.Type(), tv.Type) {
				return // reference to a declared enum const
			}
			if constant.StringVal(tv.Value) == "" {
				return // zero-value no-failure sentinel
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(expr.Pos()).Line,
				Message: "off-set constant of a reason enum type (value " + constant.StringVal(tv.Value) +
					") — a reason value must be a declared enum const, never an implicitly-converted string " +
					"literal/untyped const at a return / var / assignment / arg " +
					"(MQTT-METRIC-LABEL-VALUES-FROZEN-01 reason provenance backstop)",
			})
		}
		EachInSubtree[ast.BasicLit](file, func(expr *ast.BasicLit) { checkExpr(expr) })
		EachInSubtree[ast.Ident](file, func(expr *ast.Ident) { checkExpr(expr) })
		EachInSubtree[ast.SelectorExpr](file, func(expr *ast.SelectorExpr) { checkExpr(expr) })
		EachInSubtree[ast.BinaryExpr](file, func(expr *ast.BinaryExpr) { checkExpr(expr) })
		EachInSubtree[ast.ParenExpr](file, func(expr *ast.ParenExpr) { checkExpr(expr) })
		EachInSubtree[ast.CallExpr](file, func(expr *ast.CallExpr) { checkExpr(expr) })
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
	identicalToCollector := func(t types.Type) bool {
		if t == nil {
			return false
		}
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		for _, ct := range collectorTypes {
			if types.Identical(t, ct) {
				return true
			}
		}
		return false
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
		return identicalToCollector(info.TypeOf(fieldSel.X))
	}
	// isSanctionedWriter keys on RECEIVER TYPE identity + method name (F2, codex
	// review) — not fn.Name.Name alone. A free function or a method on some other
	// receiver named AdjustInflight / RecordPublishFailure is NOT sanctioned, so its
	// instrument writes are still flagged.
	isSanctionedWriter := func(fn *ast.FuncDecl) bool {
		if fn.Recv == nil || len(fn.Recv.List) != 1 || !mqttSanctionedWriters[fn.Name.Name] {
			return false
		}
		return identicalToCollector(info.TypeOf(fn.Recv.List[0].Type))
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Body == nil || isSanctionedWriter(fn) {
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
// funnel: callsite guard + conversion ban + reason-constant provenance backstop +
// sole-writer lock all report zero on adapters/mqtt.
func TestMqttMetricFunnel01(t *testing.T) {
	t.Parallel()
	var allDiags []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
			return nil
		}
		allDiags = append(allDiags, scanMqttReasonCallsites(p)...)
		allDiags = append(allDiags, scanMqttReasonConversions(p)...)
		allDiags = append(allDiags, scanMqttReasonOffsetConstants(p)...)
		allDiags = append(allDiags, scanMqttMetricSoleWriter(p)...)
		return nil
	})
	Report(t, "MQTT-METRIC-LABEL-VALUES-FROZEN-01", allDiags)
}

// mqttMetricsVecTypes are the kernel/observability/metrics instrument-vec types a
// provider collector field can hold.
var mqttMetricsVecTypes = map[string]bool{
	"CounterVec": true, "HistogramVec": true, "GaugeVec": true,
}

// collectMqttInstrumentFields derives, via go/types, the set of provider-collector
// fields whose type is a metrics.{Counter,Histogram,Gauge}Vec — the actual
// instrument inventory.
func collectMqttInstrumentFields(p *Pass) map[string]bool {
	info := p.TypesInfo
	if info == nil || p.Pkg == nil {
		return nil
	}
	const metricsPkg = PlatformFrameworkModulePath + "/kernel/observability/metrics"
	got := map[string]bool{}
	for _, n := range mqttCollectorStructNames {
		tn, ok := p.Pkg.Scope().Lookup(n).(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			named, ok := f.Type().(*types.Named)
			if !ok {
				continue
			}
			o := named.Obj()
			if o.Pkg() != nil && o.Pkg().Path() == metricsPkg && mqttMetricsVecTypes[o.Name()] {
				got[f.Name()] = true
			}
		}
	}
	return got
}

// TestMqttMetricInstrumentInventory01 cross-checks the hand-maintained
// mqttInstrumentFields against the go/types-derived field inventory (F2, codex
// review) — closing the staleness blind spot where a new instrument field added to
// a provider collector but not to mqttInstrumentFields would leave the sole-writer
// scanner blind to writes on it.
func TestMqttMetricInstrumentInventory01(t *testing.T) {
	t.Parallel()
	var got map[string]bool
	found := false
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
			return nil
		}
		got = collectMqttInstrumentFields(p)
		found = true
		return nil
	})
	if !found {
		t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: package %s not found", mqttPkgPath)
	}
	gotNames := make([]string, 0, len(got))
	for n := range got {
		gotNames = append(gotNames, n)
	}
	wantNames := make([]string, 0, len(mqttInstrumentFields))
	for n := range mqttInstrumentFields {
		wantNames = append(wantNames, n)
	}
	if diff := resultValuesDiff(gotNames, wantNames); diff != "" {
		t.Fatalf("MQTT-METRIC-LABEL-VALUES-FROZEN-01: instrument-field inventory drifted from "+
			"mqttInstrumentFields.\n%s\nA new metrics.*Vec field on a provider collector must be added to "+
			"mqttInstrumentFields (else the sole-writer scanner is blind to writes on it).", diff)
	}
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
//   - green                 → all four guards report 0 (declared-const callsite, no
//     conversion, const def + const-ref + "" reason returns, instrument .With only
//     in sanctioned receiver methods).
//   - red_literal           → callsite guard: inline string literal reason (1).
//   - red_conversion        → conversion ban: ConsumeFailureReason("x") (1).
//   - red_classifier_literal→ provenance backstop: classifier `return "rogue"` (1).
//   - red_typed_var         → provenance backstop: `var x Reason = "rogue"` (1).
//   - red_direct_write      → sole-writer lock: instrument .With in a free func (1).
//   - red_wrong_writer      → sole-writer lock: free func NAMED AdjustInflight (the
//     name-only bypass F2 closes) writing an instrument (1).
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
		{"green_offset_const", "green", scanMqttReasonOffsetConstants, 0},
		{"green_sole_writer", "green", scanMqttMetricSoleWriter, 0},
		{"red_literal", "red_literal", scanMqttReasonCallsites, 1},
		{"red_conversion", "red_conversion", scanMqttReasonConversions, 1},
		{"red_classifier_literal", "red_classifier_literal", scanMqttReasonOffsetConstants, 1},
		{"red_typed_var", "red_typed_var", scanMqttReasonOffsetConstants, 1},
		{"red_direct_write", "red_direct_write", scanMqttMetricSoleWriter, 1},
		{"red_wrong_writer", "red_wrong_writer", scanMqttMetricSoleWriter, 1},
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
