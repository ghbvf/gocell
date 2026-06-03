// INVARIANT: MQTT-PUBLISH-CALLSITE-FUNNEL-01
//   - INVARIANT: MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01
//   - INVARIANT: MQTT-ACK-CALLSITE-FUNNEL-01
//
// mqtt_callsite_funnel_test.go — downstream callsite funnels for the three
// autopaho/paho receive-and-send primitives that adapters/mqtt drives:
//
//   - (*autopaho.ConnectionManager).Publish  → MQTT-PUBLISH-CALLSITE-FUNNEL-01
//   - (*autopaho.ConnectionManager).Subscribe / .Unsubscribe
//     → MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01
//   - (adapters/mqtt.mqttAcker).Ack          → MQTT-ACK-CALLSITE-FUNNEL-01
//
// plus the upstream sealed-construction backstops for the token types
// (topicns.PublishableTopic / topicns.SubscribableFilter, in
// adapters/mqtt/internal/topicns) that gate the publish / subscribe paths through
// the namespace check.
//
// The PUBLISH funnel closes the gh #1225 deferred enforcement from PR-1; the
// SUBSCRIBE + ACK funnels extend the same architecture to the PR-3 receive path.
//
// # Funnel architecture (per plan §2.5 + ai-robust.md Hard 范本目录)
//
// Each funnel is a "single sanctioned holder" + (for publish/subscribe) a
// "sealed construction" composite:
//
//   - topicns.PublishableTopic / topicns.SubscribableFilter (adapters/mqtt/internal/
//     topicns): exported structs with UNEXPORTED fields. Their ONLY constructors are
//     Namespace.Mint / Namespace.MintDeadLetter (PublishableTopic) and
//     Namespace.MintFilter (SubscribableFilter), each of which internally calls
//     PublishOK / SubscribeOK. Because the fields are unexported in topicns, NO code
//     outside that package — including all of adapters/mqtt — can construct a
//     populated token, so any code holding one is guaranteed (at compile time) that
//     the topic/filter was validated against the namespace.
//   - (*Connection).Publish / (*Connection).Subscribe: the ONLY methods in
//     adapters/mqtt that call cm.Publish / cm.Subscribe. Their first non-receiver
//     parameter after ctx is topicns.PublishableTopic / topicns.SubscribableFilter —
//     so the type system gates any publish/subscribe path through the namespace check.
//   - (*Connection).ack: the ONLY method that drives manual acknowledgement.
//     autopaho v0.23.0's ConnectionManager has no Ack method (verified); manual
//     QoS1 ack routes through the package-unexported interface mqttAcker, whose
//     concrete value is the *paho.Client carried on the received PUBLISH
//     (captured by onPublishReceived into Connection.ackClient). ack is the sole
//     acker.Ack callsite.
//
// # AI-robust grading
//
// Per .claude/rules/gocell/ai-robust.md §Funnel 双向锁评级, each funnel is rated
// on two orthogonal axes (downstream / upstream):
//
//   - Downstream Hard: typed CallExpr resolution + blind-spot reverse self-checks
//     lock the callsite set. No method-value / method-expression / reflect /
//     alias escape (each is a separate reverse self-check). The detector resolves
//     the callee via go/types (ResolveMethodCall) and the enclosing FuncDecl via
//     ResolveEnclosingFunc, so a FuncLit body (e.g. the Subscribe cancel-closure)
//     is attributed to its containing FuncDecl.
//   - Upstream cross-package Hard: publishableTopic / subscribableFilter
//     unexported + Mint / MintDeadLetter / MintFilter sole constructors + (*Connection).Publish /
//     (*Connection).Subscribe parameter type — package-external code cannot
//     construct the sealed token, so cannot call the Connection method, so cannot
//     reach cm.Publish / cm.Subscribe. For ACK: (*Connection).ack is unexported,
//     Connection.ackClient (mqttAcker) is an unexported field, and *paho.Publish
//     is broker-supplied — external packages cannot reach the ack path. Type-system
//     gate, no archtest needed for the cross-package side.
//   - Upstream Hard for adapters/mqtt — #1247 DELIVERED. The token types moved to
//     adapters/mqtt/internal/topicns (PublishableTopic / SubscribableFilter), whose
//     fields are unexported there, so constructing a token carrying an arbitrary
//     topic anywhere in adapters/mqtt is a COMPILE ERROR (cross-package unexported
//     field), not an archtest finding. Unlike SPAN-SETATTR-REDACT-01 (#851) /
//     PROBENAME-SEALED-FUNNEL-01 — whose HOLDER axis is a genuine Go ceiling —
//     #1247's CONSTRUCTION axis WAS closeable, via internal/ sealed construction
//     (the marker-interface mechanism the issue originally proposed would NOT have
//     closed it; it does not gate in-package composite-literal construction).
//   - Residual Medium confined to internal/topicns (A2/A2b/A3 + S3 backstops below):
//     the only irreducible residual is that Mint / MintDeadLetter / MintFilter must
//     co-locate with the token they construct, so inside that ~1-file internal
//     package a `PublishableTopic{topic: …}` literal outside Mint is reachable; the
//     A2/A2b/A3 + S3 archtests scan THAT package (topicnsPkgPath) and lock the
//     construction allowlist + field shape — the maximal Hard reachable in Go.
//     (adapters/mqtt CAN still build the empty zero-value PublishableTopic{}: benign —
//     it carries no attacker-chosen topic and an empty topic is rejected by the
//     broker, MQTT v5 §4.7.3 — so it is not worth a separate archtest.)
//
// # Sub-rules — MQTT-PUBLISH-CALLSITE-FUNNEL-01
//
//   - A1 downstream callsite (Hard): typed CallExpr scan; `(*autopaho.ConnectionManager).Publish`
//     callees in adapters/mqtt production files ⊆ {(*Connection).Publish body}.
//   - A2 Mint construction allowlist (Medium, scoped to internal/topicns):
//     `PublishableTopic{...}` composite literal in topicnsPkgPath production files
//     ⊆ {Namespace.Mint body, Namespace.MintDeadLetter body} (MintDeadLetter for the
//     $dead/<topic> DLT sink). adapters/mqtt cannot construct the type at all (#1247).
//   - A2b field-assignment blind-spot (Medium, internal/topicns): no assignment to a
//     PublishableTopic `topic` field (`t.topic = …`) outside the Mint body.
//   - A3 PublishableTopic field freeze (Medium, internal/topicns): go/types scope
//     lock asserting NumField()==1, field name "topic", type string, unexported.
//   - A4 method-value blind-spot (Hard reverse self-check): no `cm.Publish` as
//     non-call SelectorExpr in production AST.
//   - A5 method-expression blind-spot (Hard reverse self-check): no
//     `(*autopaho.ConnectionManager).Publish(receiver, args...)` form outside
//     {(*Connection).Publish body}.
//   - A6 reflect blind-spot (Hard reverse self-check): no `reflect.Value.MethodByName`
//     in adapters/mqtt production AST with argument literal "Publish".
//   - A7 alias blind-spot (Hard reverse self-check): no `type CM = *autopaho.ConnectionManager`
//     or `type CM = autopaho.ConnectionManager` alias in adapters/mqtt.
//
// # Sub-rules — MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01
//
//   - S1 downstream Subscribe callsite (Hard): typed CallExpr scan;
//     `(*autopaho.ConnectionManager).Subscribe` callees in adapters/mqtt production
//     files ⊆ {(*Connection).sendSubscribe body}. Both (*Connection).Subscribe
//     and (*Connection).resubscribeAll route through sendSubscribe (single
//     SUBSCRIBE literal site).
//   - S2 downstream Unsubscribe callsite (Hard): `(*autopaho.ConnectionManager).Unsubscribe`
//     callees ⊆ {(*Connection).Subscribe, (*Connection).unsubscribeAll}. The
//     cancel-closure callsite is a FuncLit returned by Subscribe; ResolveEnclosingFunc
//     attributes it to the Subscribe FuncDecl.
//   - S3 MintFilter construction allowlist (Medium, scoped to internal/topicns):
//     `SubscribableFilter{...}` composite literal in topicnsPkgPath production files
//     ⊆ {Namespace.MintFilter body} + field-assignment blind-spot (no
//     `x.wireFilter = …` outside MintFilter) + field-freeze (NumField()==1, field
//     "wireFilter", string, unexported). adapters/mqtt cannot construct it (#1247).
//   - S4 method-value blind-spot (Hard reverse self-check): no `cm.Subscribe` /
//     `cm.Unsubscribe` as non-call SelectorExpr in production AST.
//   - S5 method-expression blind-spot (Hard reverse self-check): no
//     `(*autopaho.ConnectionManager).{Subscribe,Unsubscribe}(receiver, args...)`
//     form outside the allowed enclosing functions.
//   - S6 reflect blind-spot (Hard reverse self-check): no
//     reflect.Value.MethodByName("Subscribe"/"Unsubscribe") in adapters/mqtt.
//   - S7 alias blind-spot (Hard reverse self-check): shared with A7 — the
//     ConnectionManager-alias ban covers ALL cm methods (Publish/Subscribe/
//     Unsubscribe), so A7 is the single guard; no duplicate scanner.
//
// # Sub-rules — MQTT-ACK-CALLSITE-FUNNEL-01
//
//   - K1 downstream Ack callsite (Hard): typed CallExpr scan; every call to the
//     interface method `(adapters/mqtt.mqttAcker).Ack` in adapters/mqtt production
//     files ⊆ {(*Connection).ack body}. subscriber.go calls conn.ack(...) (the
//     wrapper), never acker.Ack — so subscriber does not appear in this funnel.
//   - K2 concrete-type dodge blind-spot (Hard): no direct
//     `(*github.com/eclipse/paho.golang/paho.Client).Ack` callsite anywhere in
//     adapters/mqtt production code — closes the "interface dodge" where one
//     bypasses mqttAcker by calling the concrete *paho.Client.Ack.
//   - K3 method-value / method-expression / reflect blind-spots (Hard reverse
//     self-checks): no `acker.Ack` method-value, no method-expression, no
//     reflect.MethodByName("Ack") for the Ack method in adapters/mqtt production
//     files outside (*Connection).ack.
//
// # Blind-spot inventory rationale (per ai-robust.md §载体决策原则)
//
// The downstream Hard rating requires explicit reverse self-checks for AST forms
// outside the declared coverage of typed CallExpr resolution. The forms
// (method-value, method-expression, reflect, alias, concrete-type dodge) are
// precisely the ways a cm/acker method could be invoked without appearing as a
// direct CallExpr `recv.Method(...)`. Each gets a sub-test that walks production
// AST and asserts the form does not appear. Every scanner has a *_ScannerNonVacuous
// companion proving it finds ≥1 real target, so an empty scan cannot vacuously pass.
//
// ref: gh #1225 — PR-1 deferred enforcement (publish funnel)
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md
// ref: ai-robust.md §Hard 范本目录 "single sanctioned holder" + "string-typed concept funnel"
// ref: ai-robust.md §Funnel 双向锁评级
// ref: gh #1247 — package-internal Medium→Hard via internal/topicns sealed construction (A2/A2b/A3/S3 now scope topicnsPkgPath)
// ref: SPAN-SETATTR-REDACT-01 (sibling Hard funnel pattern; gh #851)
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/mqtt"
)

// autophoPkgPath is the import path of the autopaho package that owns ConnectionManager.
const autophoPkgPath = "github.com/eclipse/paho.golang/autopaho"

// pahoPkgPath is the import path of the paho package that owns Client.
const pahoPkgPath = "github.com/eclipse/paho.golang/paho"

// connectionManagerTypeName is the type whose Publish/Subscribe/Unsubscribe
// methods we funnel.
const connectionManagerTypeName = "ConnectionManager"

// publishMethodName / subscribeMethodName / unsubscribeMethodName / ackMethodName
// are the callee method names we funnel.
const (
	publishMethodName     = "Publish"
	subscribeMethodName   = "Subscribe"
	unsubscribeMethodName = "Unsubscribe"
	ackMethodName         = "Ack"
)

// cmPublishFullName / cmSubscribeFullName / cmUnsubscribeFullName are the
// *types.Func.FullName() of the autopaho ConnectionManager targets. These are
// the canonical keys used by ResolveMethodCall-based checks.
const (
	cmPublishFullName     = "(*github.com/eclipse/paho.golang/autopaho.ConnectionManager).Publish"
	cmSubscribeFullName   = "(*github.com/eclipse/paho.golang/autopaho.ConnectionManager).Subscribe"
	cmUnsubscribeFullName = "(*github.com/eclipse/paho.golang/autopaho.ConnectionManager).Unsubscribe"
)

// mqttAckerAckFullName is the *types.Func.FullName() of the package-unexported
// mqttAcker interface's Ack method. pahoClientAckFullName is the concrete
// *paho.Client.Ack that K2 bans direct callsites of.
const (
	mqttAckerAckFullName  = "(github.com/ghbvf/gocell/adapters/mqtt.mqttAcker).Ack"
	pahoClientAckFullName = "(*github.com/eclipse/paho.golang/paho.Client).Ack"
)

// Enclosing-func keys: the FullName() of the sole allowed enclosing function(s)
// for each callsite, resolved via ResolveEnclosingFunc.
const (
	connectionPublishEnclosingFuncKey        = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).Publish"
	connectionSendSubscribeEnclosingFuncKey  = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).sendSubscribe"
	connectionSubscribeEnclosingFuncKey      = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).Subscribe"
	connectionUnsubscribeAllEnclosingFuncKey = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).unsubscribeAll"
	connectionAckEnclosingFuncKey            = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).ack"
)

// topicnsPkgPath is the internal sub-package that now owns the sealed token
// types (PublishableTopic / SubscribableFilter) and their validated
// constructors. Since #1247 the composite-literal axis is compile-Hard for
// adapters/mqtt (cross-package unexported fields); the A2 / A2b / A3 (and S3)
// token-construction archtests below therefore scan THIS package — the residual
// (Mint must co-locate with the token it constructs) is an irreducible Medium
// confined to this ~1-file package, the maximal Hard reachable in Go.
const topicnsPkgPath = mqttPkgPath + "/internal/topicns"

// Typed FullName() keys for the sanctioned token constructors in internal/topicns.
// These are methods on topicns.Namespace, so the FullName has a value-receiver
// prefix "(pkgPath.TypeName).MethodName".
const (
	mintFullName           = "(github.com/ghbvf/gocell/adapters/mqtt/internal/topicns.Namespace).Mint"
	mintDeadLetterFullName = "(github.com/ghbvf/gocell/adapters/mqtt/internal/topicns.Namespace).MintDeadLetter"
	mintFilterFullName     = "(github.com/ghbvf/gocell/adapters/mqtt/internal/topicns.Namespace).MintFilter"
)

// mqttTokenFieldFixturePkgPath is the fixture package for F2 field-assignment
// non-vacuity (typed scanner). Anchored to PlatformModulePath.
const mqttTokenFieldFixturePkgPath = PlatformModulePath + "/tools/archtest/internal/mqtttokenfieldfixture"

// ─── Shared callsite-funnel scanner ──────────────────────────────────────────

// scanMQTTCallsiteFunnel walks the adapters/mqtt production AST and reports a
// diagnostic for every CallExpr whose resolved callee FullName() == targetFull
// but whose enclosing FuncDecl is NOT in allowedKeys (set semantics). This is
// the generalized form of the publish A1 scanner, shared by A1 / S1 / S2 / K1 /
// K2. Returns the diagnostics plus the count of matched callsites (for the
// non-vacuous companions).
func scanMQTTCallsiteFunnel(t *testing.T, ruleID, targetFull string, allowedKeys []string) ([]Diagnostic, int) {
	t.Helper()
	var diags []Diagnostic
	var matched int

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					fn, ok := ResolveMethodCall(p.TypesInfo, sel)
					if !ok || fn == nil || fn.FullName() != targetFull {
						return
					}
					matched++
					if mqttEnclosingKeyAllowed(p, f, call, allowedKeys) {
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: %s callsite at %s:%d is outside the allowed funnel %v",
							ruleID, targetFull, rel, pos.Line, allowedKeys,
						),
					})
				})
			}
			return nil
		})

	return diags, matched
}

// mqttEnclosingKeyAllowed reports whether the enclosing FuncDecl of node resolves
// to one of allowedKeys. A FuncLit body (e.g. the Subscribe cancel-closure) is
// attributed to its containing FuncDecl by ResolveEnclosingFunc.
func mqttEnclosingKeyAllowed(p *Pass, f *ast.File, node ast.Node, allowedKeys []string) bool {
	enclosing, ok := ResolveEnclosingFunc(p.TypesInfo, f, node)
	if !ok || enclosing == nil {
		return false
	}
	for _, k := range allowedKeys {
		if enclosing.FullName() == k {
			return true
		}
	}
	return false
}

// ─── A1: downstream cm.Publish callsite Hard ─────────────────────────────────

// TestMQTTPublishCallsiteFunnel_A1_DownstreamCMPublishCallsite enforces that
// every call to (*autopaho.ConnectionManager).Publish in adapters/mqtt
// production files is enclosed by (*Connection).Publish and no other function.
func TestMQTTPublishCallsiteFunnel_A1_DownstreamCMPublishCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags, _ := scanMQTTCallsiteFunnel(t, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1",
		cmPublishFullName, []string{connectionPublishEnclosingFuncKey})
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1: cm.Publish callsites outside (*Connection).Publish detected")
}

// TestMQTTPublishCallsiteFunnel_A1_ScannerNonVacuous asserts the A1 scanner finds
// ≥1 cm.Publish callsite. If zero are found, the scanner is silently broken.
func TestMQTTPublishCallsiteFunnel_A1_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	_, matched := scanMQTTCallsiteFunnel(t, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1",
		cmPublishFullName, []string{connectionPublishEnclosingFuncKey})
	assert.GreaterOrEqual(t, matched, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1: scanner found 0 cm.Publish callsites — "+
			"the go/types resolution path may be broken, or Connection.Publish no longer calls cm.Publish")
}

// ─── S1: downstream cm.Subscribe callsite Hard ───────────────────────────────

// TestMQTTSubscribeCallsiteFunnel_S1_DownstreamCMSubscribeCallsite enforces that
// every call to (*autopaho.ConnectionManager).Subscribe in adapters/mqtt
// production files is enclosed by (*Connection).sendSubscribe and no other
// function. Both (*Connection).Subscribe and resubscribeAll route through it.
func TestMQTTSubscribeCallsiteFunnel_S1_DownstreamCMSubscribeCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags, _ := scanMQTTCallsiteFunnel(t, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S1",
		cmSubscribeFullName, []string{connectionSendSubscribeEnclosingFuncKey})
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S1: cm.Subscribe callsites outside (*Connection).sendSubscribe detected")
}

func TestMQTTSubscribeCallsiteFunnel_S1_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	_, matched := scanMQTTCallsiteFunnel(t, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S1",
		cmSubscribeFullName, []string{connectionSendSubscribeEnclosingFuncKey})
	assert.GreaterOrEqual(t, matched, 1,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S1: scanner found 0 cm.Subscribe callsites — "+
			"the go/types resolution path may be broken, or sendSubscribe no longer calls cm.Subscribe")
}

// ─── S2: downstream cm.Unsubscribe callsite Hard ─────────────────────────────

// TestMQTTSubscribeCallsiteFunnel_S2_DownstreamCMUnsubscribeCallsite enforces
// that every call to (*autopaho.ConnectionManager).Unsubscribe is enclosed by
// either (*Connection).Subscribe (the cancel-closure FuncLit) or
// (*Connection).unsubscribeAll. The cancel-closure callsite's enclosing FuncDecl
// resolves to Subscribe via ResolveEnclosingFunc.
func TestMQTTSubscribeCallsiteFunnel_S2_DownstreamCMUnsubscribeCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	allowed := []string{connectionSubscribeEnclosingFuncKey, connectionUnsubscribeAllEnclosingFuncKey}
	diags, _ := scanMQTTCallsiteFunnel(t, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S2",
		cmUnsubscribeFullName, allowed)
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S2: cm.Unsubscribe callsites outside "+
			"{(*Connection).Subscribe, (*Connection).unsubscribeAll} detected")
}

func TestMQTTSubscribeCallsiteFunnel_S2_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	allowed := []string{connectionSubscribeEnclosingFuncKey, connectionUnsubscribeAllEnclosingFuncKey}
	_, matched := scanMQTTCallsiteFunnel(t, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S2",
		cmUnsubscribeFullName, allowed)
	assert.GreaterOrEqual(t, matched, 2,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S2: scanner found <2 cm.Unsubscribe callsites — "+
			"there are exactly two (cancel-closure in Subscribe + unsubscribeAll); "+
			"the go/types resolution path may be broken")
}

// ─── K1: downstream mqttAcker.Ack callsite Hard ──────────────────────────────

// TestMQTTAckCallsiteFunnel_K1_DownstreamAckerAckCallsite enforces that every
// call to the package-unexported interface method (mqtt.mqttAcker).Ack in
// adapters/mqtt production files is enclosed by (*Connection).ack and no other
// function. ResolveMethodCall resolves acker.Ack to the interface method
// full-name "(<pkgpath>.mqttAcker).Ack" (no leading "*" for interface methods).
func TestMQTTAckCallsiteFunnel_K1_DownstreamAckerAckCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags, _ := scanMQTTCallsiteFunnel(t, "MQTT-ACK-CALLSITE-FUNNEL-01/K1",
		mqttAckerAckFullName, []string{connectionAckEnclosingFuncKey})
	assert.Empty(t, diags,
		"MQTT-ACK-CALLSITE-FUNNEL-01/K1: mqttAcker.Ack callsites outside (*Connection).ack detected")
}

func TestMQTTAckCallsiteFunnel_K1_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	_, matched := scanMQTTCallsiteFunnel(t, "MQTT-ACK-CALLSITE-FUNNEL-01/K1",
		mqttAckerAckFullName, []string{connectionAckEnclosingFuncKey})
	assert.GreaterOrEqual(t, matched, 1,
		"MQTT-ACK-CALLSITE-FUNNEL-01/K1: scanner found 0 mqttAcker.Ack callsites — "+
			"the interface-method resolution path may be broken (verify mqttAckerAckFullName), "+
			"or (*Connection).ack no longer calls acker.Ack")
}

// ─── K2: concrete *paho.Client.Ack dodge blind-spot Hard ─────────────────────

// TestMQTTAckCallsiteFunnel_K2_NoDirectPahoClientAck closes the "interface dodge":
// nobody may bypass mqttAcker by calling the concrete (*paho.Client).Ack
// directly. There must be ZERO such callsites in adapters/mqtt production code.
func TestMQTTAckCallsiteFunnel_K2_NoDirectPahoClientAck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags, _ := scanMQTTCallsiteFunnel(t, "MQTT-ACK-CALLSITE-FUNNEL-01/K2",
		pahoClientAckFullName, nil /* allowlist empty: zero callsites permitted */)
	assert.Empty(t, diags,
		"MQTT-ACK-CALLSITE-FUNNEL-01/K2: direct (*paho.Client).Ack callsite detected in adapters/mqtt — "+
			"ack must route through the mqttAcker interface via (*Connection).ack")
}

// TestMQTTAckCallsiteFunnel_K2_ScannerNonVacuous proves the K2 detector's
// callee-resolution path resolves a *paho.Client.Ack call. Since production
// MUST NOT contain such a call, this is proven against a synthetic in-memory
// snippet (the same discipline as the AST-mechanism non-vacuous proofs) rather
// than the production tree.
func TestMQTTAckCallsiteFunnel_K2_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	// A pure-AST sanity check: a `c.Ack(pb)` selector with Sel "Ack" is the form
	// the K2 typed scanner resolves. We prove the SelectorExpr/Sel-name precursor
	// fires; the go/types callee resolution itself is the same ResolveMethodCall
	// path proven non-vacuous by K1 (which DOES find a real interface Ack call).
	const src = `package x
type client struct{}
func (client) Ack(pb any) error { return nil }
func f(c client, pb any) { _ = c.Ack(pb) }
`
	f := mqttParseSnippet(t, src)
	var fired bool
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == ackMethodName {
			fired = true
		}
	})
	assert.True(t, fired,
		"MQTT-ACK-CALLSITE-FUNNEL-01/K2: the `_.Ack(...)` CallExpr precursor did not fire on a "+
			"known Ack call snippet — the scanner traversal is broken (K2 would pass vacuously)")
}

// ─── A2 / S3: sealed-token construction allowlist (Medium) ───────────────────

// TestMQTTPublishCallsiteFunnel_A2_PublishableTopicConstructionAllowlist enforces
// that non-zero publishableTopic composite literals in adapters/mqtt production
// files are only inside the two sanctioned constructors TopicNamespace.Mint and
// TopicNamespace.MintDeadLetter (PR-4 $dead/<topic> DLT sink). Both mint through
// PublishOK; no other callsite may construct the sealed token.
func TestMQTTPublishCallsiteFunnel_A2_PublishableTopicConstructionAllowlist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTTokenConstruction(t, "PublishableTopic",
		[]string{mintFullName, mintDeadLetterFullName},
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2")
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2: publishableTopic composite literals outside Mint/MintDeadLetter body detected")
}

func TestMQTTPublishCallsiteFunnel_A2_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	inside := countMQTTTokenLiteralsInsideFunc(t, "PublishableTopic", mintFullName)
	assert.GreaterOrEqual(t, inside, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2: scanner found 0 publishableTopic literals inside Mint — "+
			"the go/types resolution path may be silently broken")
}

// TestMQTTPublishCallsiteFunnel_A2_ScannerNonVacuous_MintDeadLetter asserts the
// second sanctioned constructor actually mints through the sealed publishableTopic
// type (≥1 composite literal inside MintDeadLetter). This is the blind-spot
// reverse-check for the A2 allowlist extension: it fails if MintDeadLetter is
// renamed, deleted, or stops constructing the sealed token (e.g. bypasses it),
// guarding against a stale allowlist entry that silently permits nothing.
func TestMQTTPublishCallsiteFunnel_A2_ScannerNonVacuous_MintDeadLetter(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	inside := countMQTTTokenLiteralsInsideFunc(t, "PublishableTopic", mintDeadLetterFullName)
	assert.GreaterOrEqual(t, inside, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2: scanner found 0 publishableTopic literals inside MintDeadLetter — "+
			"MintDeadLetter must mint through the sealed publishableTopic type; the allowlist entry is stale or the resolution path is broken")
}

// TestMQTTSubscribeCallsiteFunnel_S3_SubscribableFilterConstructionAllowlist
// enforces that non-zero subscribableFilter composite literals in adapters/mqtt
// production files are only inside TopicNamespace.MintFilter.
func TestMQTTSubscribeCallsiteFunnel_S3_SubscribableFilterConstructionAllowlist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTTokenConstruction(t, "SubscribableFilter",
		[]string{mintFilterFullName},
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3")
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3: subscribableFilter composite literals outside MintFilter body detected")
}

func TestMQTTSubscribeCallsiteFunnel_S3_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	inside := countMQTTTokenLiteralsInsideFunc(t, "SubscribableFilter", mintFilterFullName)
	assert.GreaterOrEqual(t, inside, 1,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3: scanner found 0 subscribableFilter literals inside MintFilter — "+
			"the go/types resolution path may be silently broken")
}

// scanMQTTTokenConstruction reports non-zero composite literals of the named
// adapters/mqtt token type outside the allowed constructor functions.
func scanMQTTTokenConstruction(t *testing.T, typeName string, allowedFuncs []string, ruleID string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{topicnsPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != topicnsPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanSealedCompositeLitConstruction(
					p.Fset, f, rel, p.TypesInfo, topicnsPkgPath, typeName, allowedFuncs, ruleID,
				)...)
			}
			return nil
		})

	return diags
}

// countMQTTTokenLiteralsInsideFunc counts non-zero composite literals of the
// named adapters/mqtt token type whose enclosing function's FullName() matches
// funcFullName (typed identity via ResolveEnclosingFunc).
func countMQTTTokenLiteralsInsideFunc(t *testing.T, typeName, funcFullName string) int {
	t.Helper()
	var inside int
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{topicnsPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != topicnsPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
					if lit.Type == nil || len(lit.Elts) == 0 {
						return
					}
					if !mqttCompositeLitIsType(p.TypesInfo, lit, typeName) {
						return
					}
					// Use typed FullName() identity instead of bare name comparison.
					if mqttEnclosingFuncAllowed(p.TypesInfo, f, lit, []string{funcFullName}) {
						inside++
					}
				})
			}
			return nil
		})

	return inside
}

// mqttCompositeLitIsType reports whether lit constructs the named adapters/mqtt type.
func mqttCompositeLitIsType(info *types.Info, lit *ast.CompositeLit, typeName string) bool {
	tv, ok := info.Types[lit.Type]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil && tobj.Pkg().Path() == topicnsPkgPath && tobj.Name() == typeName
}

// ─── A2b / S3-fieldassign: field-assignment blind-spots (Medium) ─────────────

// TestMQTTPublishCallsiteFunnel_A2b_NoFieldAssignmentBypass bans any assignment
// to a publishableTopic `topic` field outside the Mint body.
func TestMQTTPublishCallsiteFunnel_A2b_NoFieldAssignmentBypass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTTokenFieldAssignment(t, "PublishableTopic",
		[]string{"topic"}, mintFullName, topicnsPkgPath, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2b")
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2b: publishableTopic.topic field assignment outside Mint detected")
}

// TestMQTTSubscribeCallsiteFunnel_S3_NoFieldAssignmentBypass bans any assignment
// to a subscribableFilter wireFilter field outside the MintFilter body.
func TestMQTTSubscribeCallsiteFunnel_S3_NoFieldAssignmentBypass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTTokenFieldAssignment(t, "SubscribableFilter",
		[]string{"wireFilter"}, mintFilterFullName, topicnsPkgPath, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3")
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3: subscribableFilter field assignment outside MintFilter detected")
}

// scanMQTTTokenFieldAssignment reports assignments to one of fieldNames on a
// receiver typed as the named token type (in targetPkgPath), outside the
// allowed function (matched by typed FullName() identity).
//
// The targetPkgPath parameter lets the production scanner target topicnsPkgPath,
// while the F2 fixture test targets its own fixture package — both prove the
// real typed scanner fires.
func scanMQTTTokenFieldAssignment(
	t *testing.T, typeName string, fieldNames []string,
	allowedFuncFullName, targetPkgPath, ruleID string,
) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{targetPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != targetPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.AssignStmt](f, func(assign *ast.AssignStmt) {
					for _, lhs := range assign.Lhs {
						if d, ok := mqttTokenFieldAssignDiag(p, f, rel, assign, lhs,
							typeName, fieldNames, allowedFuncFullName, targetPkgPath, ruleID); ok {
							diags = append(diags, d)
						}
					}
				})
			}
			return nil
		})

	return diags
}

// mqttTokenFieldAssignDiag returns a diagnostic (ok=true) when lhs is an
// assignment to one of fieldNames on a receiver typed as the named token (in
// targetPkgPath), outside allowedFuncFullName. The enclosing-func gate uses
// typed FullName() identity via mqttEnclosingKeyAllowed. Extracted from
// scanMQTTTokenFieldAssignment to keep cognitive complexity under budget.
func mqttTokenFieldAssignDiag(
	p *Pass, f *ast.File, rel string, assign *ast.AssignStmt, lhs ast.Expr,
	typeName string, fieldNames []string, allowedFuncFullName, targetPkgPath, ruleID string,
) (Diagnostic, bool) {
	sel, ok := lhs.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || !mqttInStringSet(sel.Sel.Name, fieldNames) {
		return Diagnostic{}, false
	}
	if !mqttIsTokenTyped(p.TypesInfo, sel.X, typeName, targetPkgPath) {
		return Diagnostic{}, false
	}
	// Typed identity gate: compare by FullName() not bare name.
	if mqttEnclosingKeyAllowed(p, f, assign, []string{allowedFuncFullName}) {
		return Diagnostic{}, false
	}
	pos := p.Fset.Position(sel.Pos())
	return Diagnostic{
		Rel:  rel,
		Line: pos.Line,
		Message: fmt.Sprintf(
			"%s: assignment to %s.%s at %s:%d outside %s — "+
				"field-assignment bypasses namespace validation; construct via %s only",
			ruleID, typeName, sel.Sel.Name, rel, pos.Line, allowedFuncFullName, allowedFuncFullName,
		),
	}, true
}

func mqttInStringSet(s string, set []string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// mqttIsTokenTyped reports whether expr has type typeName (value or pointer)
// declared in targetPkgPath. The targetPkgPath parameter allows the scanner
// to be used for both production (topicnsPkgPath) and fixture packages (F2).
func mqttIsTokenTyped(info *types.Info, expr ast.Expr, typeName, targetPkgPath string) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	typ := tv.Type
	if ptr, isPtr := typ.(*types.Pointer); isPtr {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil && tobj.Pkg().Path() == targetPkgPath && tobj.Name() == typeName
}

// mqttAssertTypedFieldAssignScannerFires proves the real typed field-assignment
// scanner primitives (mqttIsTokenTyped + mqttEnclosingKeyAllowed) fire on a genuine
// outside-allowed-func violation AND do not report the inside-allowed-func
// assignment. It uses the #1287 K2 pattern: the mqtttokenfieldfixture package holds
// a shape-replica of each token type whose field CAN be assigned cross-function
// (unlike the production token whose field is unexported in internal/topicns —
// making cross-func assignment within the package impossible via the type system
// but still reachable by the archtest). Both A2b (publish, PublishableTopic/topic)
// and S3 (subscribe, SubscribableFilter/wireFilter) share this single mechanism, so
// each side's field-assign non-vacuity is proven end-to-end on real source.
//
// The fixture is at tools/archtest/internal/mqtttokenfieldfixture/fixture.go.
func mqttAssertTypedFieldAssignScannerFires(t *testing.T, typeName, fieldName, allowedCtorFullName, ruleID string) {
	t.Helper()

	var violations []Diagnostic
	var greenCount int

	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{mqttTokenFieldFixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttTokenFieldFixturePkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.AssignStmt](f, func(assign *ast.AssignStmt) {
					for _, lhs := range assign.Lhs {
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok || sel.Sel == nil || sel.Sel.Name != fieldName {
							continue
						}
						if !mqttIsTokenTyped(p.TypesInfo, sel.X, typeName, mqttTokenFieldFixturePkgPath) {
							continue
						}
						if mqttEnclosingKeyAllowed(p, f, assign, []string{allowedCtorFullName}) {
							greenCount++ // inside-allowed: must not be reported
							continue
						}
						pos := p.Fset.Position(sel.Pos())
						violations = append(violations, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"%s: outside-allowed field assignment at %s:%d (red fixture site)",
								ruleID, rel, pos.Line,
							),
						})
					}
				})
			}
			return nil
		})

	require.NotEmpty(t, violations,
		"%s typed scanner must report ≥1 violation on the red fixture "+
			"(an outside-allowed %s.%s assignment) — if this fails, mqttIsTokenTyped "+
			"or the typed enclosing-func gate is broken", ruleID, typeName, fieldName)

	// The green site (inside the allowed constructor) must be seen but not reported.
	assert.GreaterOrEqual(t, greenCount, 1,
		"%s typed scanner: the green site inside %s was not seen — "+
			"mqttIsTokenTyped may not resolve the fixture type", ruleID, allowedCtorFullName)
}

// TestMQTTPublishCallsiteFunnel_A2b_ScannerNonVacuous_Typed proves the publish-side
// (PublishableTopic / topic) field-assign scanner is non-vacuous via the shared
// typed fixture mechanism.
func TestMQTTPublishCallsiteFunnel_A2b_ScannerNonVacuous_Typed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	mqttAssertTypedFieldAssignScannerFires(t, "PublishableTopic", "topic",
		mqttTokenFieldFixturePkgPath+".ParseFixtureTopic", "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2b")
}

// TestMQTTSubscribeCallsiteFunnel_S3_FieldAssignScannerNonVacuous proves the
// subscribe-side (SubscribableFilter / wireFilter) field-assign scanner is
// non-vacuous via the same shared typed fixture mechanism as A2b. The S3 production
// scan (TestMQTTSubscribeCallsiteFunnel_S3_NoFieldAssignmentBypass) is clean because
// MintFilter constructs via a composite literal rather than a separate field
// assignment, so this fixture-backed test is the load-bearing non-vacuity proof for
// the subscribe side (rather than delegating to A2b).
func TestMQTTSubscribeCallsiteFunnel_S3_FieldAssignScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	mqttAssertTypedFieldAssignScannerFires(t, "SubscribableFilter", "wireFilter",
		mqttTokenFieldFixturePkgPath+".MintFixtureFilter", "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3")
}

// ─── A3 / S3-fieldfreeze: token field freeze (Medium) ─────────────────────────

// TestMQTTPublishCallsiteFunnel_A3_PublishableTopicFieldFreeze locks the shape of
// publishableTopic: NumFields==1, field "topic", string, unexported.
func TestMQTTPublishCallsiteFunnel_A3_PublishableTopicFieldFreeze(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	mqttAssertTokenFieldFreeze(t, "PublishableTopic",
		[]mqttFieldSpec{{name: "topic"}}, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A3")
}

// TestMQTTSubscribeCallsiteFunnel_S3_SubscribableFilterFieldFreeze locks the
// shape of subscribableFilter: NumFields==1, field "wireFilter", string,
// unexported. (matchFilter was removed when dispatch moved to MQTT v5
// Subscription Identifiers — the broker matches the topic, the client routes by
// sub-id, so a client-side bare-filter shape is no longer carried.)
func TestMQTTSubscribeCallsiteFunnel_S3_SubscribableFilterFieldFreeze(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	mqttAssertTokenFieldFreeze(t, "SubscribableFilter",
		[]mqttFieldSpec{{name: "wireFilter"}},
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3")
}

type mqttFieldSpec struct{ name string }

// mqttAssertTokenFieldFreeze verifies, via go/types scope lookup, that the named
// adapters/mqtt token type is a struct whose fields exactly match wantFields
// (order-sensitive), each string-typed and unexported.
func mqttAssertTokenFieldFreeze(t *testing.T, typeName string, wantFields []mqttFieldSpec, ruleID string) {
	t.Helper()
	var checked bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{topicnsPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != topicnsPkgPath {
				return nil
			}
			obj := p.Pkg.Scope().Lookup(typeName)
			if obj == nil {
				return nil
			}
			checked = true
			tn, ok := obj.(*types.TypeName)
			if !ok {
				t.Errorf("%s: %s scope object is not *types.TypeName", ruleID, typeName)
				return nil
			}
			st, ok := tn.Type().Underlying().(*types.Struct)
			if !ok {
				t.Errorf("%s: %s underlying type is %T, want *types.Struct — "+
					"type may have been changed to a string newtype or alias",
					ruleID, typeName, tn.Type().Underlying())
				return nil
			}
			if st.NumFields() != len(wantFields) {
				t.Errorf("%s: %s NumFields = %d, want %d — "+
					"field-set change re-opens the sealed-construction invariant",
					ruleID, typeName, st.NumFields(), len(wantFields))
				return nil
			}
			for i, want := range wantFields {
				fld := st.Field(i)
				if fld.Name() != want.name {
					t.Errorf("%s: %s field[%d] name = %q, want %q",
						ruleID, typeName, i, fld.Name(), want.name)
				}
				if fld.Exported() {
					t.Errorf("%s: %s.%s is exported — package-external literal "+
						"construction would become possible; unexport it",
						ruleID, typeName, fld.Name())
				}
				basic, isBasic := fld.Type().(*types.Basic)
				if !isBasic || basic.Kind() != types.String {
					t.Errorf("%s: %s.%s type is %v, want string",
						ruleID, typeName, fld.Name(), fld.Type())
				}
			}
			return nil
		})

	require.True(t, checked,
		"%s: %s not found in adapters/mqtt scope — type may have been renamed or deleted",
		ruleID, typeName)
}

// ─── A4 / S4: method-value blind-spots (Hard reverse self-checks) ────────────

// TestMQTTPublishCallsiteFunnel_A4_BlindSpot_NoMethodValue asserts cm.Publish is
// never used as a method-value (non-call SelectorExpr) in adapters/mqtt.
func TestMQTTPublishCallsiteFunnel_A4_BlindSpot_NoMethodValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTCMMethodValue(t, []string{publishMethodName}, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A4")
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A4: cm.Publish used as method-value in adapters/mqtt production code")
}

// TestMQTTSubscribeCallsiteFunnel_S4_BlindSpot_NoMethodValue asserts
// cm.Subscribe / cm.Unsubscribe are never used as method-values.
func TestMQTTSubscribeCallsiteFunnel_S4_BlindSpot_NoMethodValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTCMMethodValue(t, []string{subscribeMethodName, unsubscribeMethodName},
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S4")
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S4: cm.Subscribe/Unsubscribe used as method-value in adapters/mqtt production code")
}

// scanMQTTCMMethodValue walks production AST for SelectorExpr nodes whose receiver
// type is *autopaho.ConnectionManager and Sel ∈ methodNames, reporting any that
// are NOT the Fun of a direct CallExpr (i.e. method-value extraction).
func scanMQTTCMMethodValue(t *testing.T, methodNames []string, ruleID string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				directCallFuns := make(map[*ast.SelectorExpr]bool)
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						directCallFuns[sel] = true
					}
				})

				EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
					if sel.Sel == nil || !mqttInStringSet(sel.Sel.Name, methodNames) {
						return
					}
					if !isCMReceiver(p.TypesInfo, sel.X) {
						return
					}
					if directCallFuns[sel] {
						return
					}
					pos := p.Fset.Position(sel.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: cm.%s at %s:%d used as a method-value (not a direct call) — "+
								"forbidden: all cm method invocations must go through the sanctioned funnel",
							ruleID, sel.Sel.Name, rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	return diags
}

// TestMQTTCallsiteFunnel_A4S4_ScannerNonVacuous proves isCMReceiver resolves the
// receiver of a real production cm method selector (cm.Publish / cm.Subscribe /
// cm.Unsubscribe) to *autopaho.ConnectionManager. If it returns 0, the
// method-value detectors A4/S4 would vacuously pass.
func TestMQTTCallsiteFunnel_A4S4_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var hits int
	cmMethods := []string{publishMethodName, subscribeMethodName, unsubscribeMethodName}
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
					if sel.Sel == nil || !mqttInStringSet(sel.Sel.Name, cmMethods) {
						return
					}
					if isCMReceiver(p.TypesInfo, sel.X) {
						hits++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, hits, 1,
		"MQTT-{PUBLISH,SUBSCRIBE}-CALLSITE-FUNNEL-01/A4,S4: isCMReceiver found 0 "+
			"*autopaho.ConnectionManager receivers of a Publish/Subscribe/Unsubscribe selector — "+
			"the go/types receiver-resolution path may be broken")
}

// ─── A5 / S5: method-expression blind-spots (Hard reverse self-checks) ───────

// TestMQTTPublishCallsiteFunnel_A5_BlindSpot_NoMethodExpression asserts the
// method-expression form (*autopaho.ConnectionManager).Publish(recv, ...) does
// not appear outside (*Connection).Publish.
func TestMQTTPublishCallsiteFunnel_A5_BlindSpot_NoMethodExpression(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTCMMethodExpression(t, []string{publishMethodName},
		[]string{connectionPublishEnclosingFuncKey}, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A5")
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A5: method-expression cm.Publish detected outside (*Connection).Publish")
}

// TestMQTTSubscribeCallsiteFunnel_S5_BlindSpot_NoMethodExpression asserts the
// method-expression forms for Subscribe / Unsubscribe do not appear outside
// their allowed enclosing functions.
func TestMQTTSubscribeCallsiteFunnel_S5_BlindSpot_NoMethodExpression(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	allowed := []string{
		connectionSendSubscribeEnclosingFuncKey,
		connectionSubscribeEnclosingFuncKey,
		connectionUnsubscribeAllEnclosingFuncKey,
	}
	diags := scanMQTTCMMethodExpression(t, []string{subscribeMethodName, unsubscribeMethodName},
		allowed, "MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S5")
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S5: method-expression cm.Subscribe/Unsubscribe detected outside the allowed funnel")
}

// scanMQTTCMMethodExpression walks production AST for method-expression CallExprs
// (*autopaho.ConnectionManager).<method>(recv, ...) with method ∈ methodNames,
// reporting any whose enclosing function is not in allowedKeys.
func scanMQTTCMMethodExpression(t *testing.T, methodNames, allowedKeys []string, ruleID string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isMethodExpressionCMCall(call, methodNames) {
						return
					}
					if mqttEnclosingKeyAllowed(p, f, call, allowedKeys) {
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: method-expression (*autopaho.ConnectionManager) call at %s:%d "+
								"outside the allowed funnel %v — forbidden",
							ruleID, rel, pos.Line, allowedKeys,
						),
					})
				})
			}
			return nil
		})

	return diags
}

func TestMQTTCallsiteFunnel_A5S5_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	const src = `package x
import "github.com/eclipse/paho.golang/autopaho"
func f(cm *autopaho.ConnectionManager) {
	(*autopaho.ConnectionManager).Publish(cm, nil)
	(*autopaho.ConnectionManager).Subscribe(cm, nil)
	(*autopaho.ConnectionManager).Unsubscribe(cm, nil)
}
`
	f := mqttParseSnippet(t, src)
	var fired int
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if isMethodExpressionCMCall(call, []string{publishMethodName, subscribeMethodName, unsubscribeMethodName}) {
			fired++
		}
	})
	assert.GreaterOrEqual(t, fired, 3,
		"MQTT-{PUBLISH,SUBSCRIBE}-CALLSITE-FUNNEL-01/A5,S5: isMethodExpressionCMCall did not fire on the "+
			"known method-expression calls — the detector is broken (A5/S5 would pass vacuously)")
}

// ─── A6 / S6 / K3: reflect MethodByName blind-spots (Hard reverse self-checks) ─

// TestMQTTPublishCallsiteFunnel_A6_BlindSpot_NoReflectMethodByName bans
// reflect.Value.MethodByName("Publish") in adapters/mqtt.
func TestMQTTPublishCallsiteFunnel_A6_BlindSpot_NoReflectMethodByName(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReflectMethodByName(t, []string{publishMethodName}, "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A6")
	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A6: reflect.MethodByName(\"Publish\") detected in adapters/mqtt production code")
}

// TestMQTTSubscribeCallsiteFunnel_S6_BlindSpot_NoReflectMethodByName bans
// reflect.Value.MethodByName("Subscribe"/"Unsubscribe") in adapters/mqtt.
func TestMQTTSubscribeCallsiteFunnel_S6_BlindSpot_NoReflectMethodByName(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReflectMethodByName(t, []string{subscribeMethodName, unsubscribeMethodName},
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S6")
	assert.Empty(t, diags,
		"MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S6: reflect.MethodByName(\"Subscribe\"/\"Unsubscribe\") detected in adapters/mqtt production code")
}

// TestMQTTAckCallsiteFunnel_K3_BlindSpot_NoReflectMethodByName bans
// reflect.Value.MethodByName("Ack") in adapters/mqtt.
func TestMQTTAckCallsiteFunnel_K3_BlindSpot_NoReflectMethodByName(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReflectMethodByName(t, []string{ackMethodName}, "MQTT-ACK-CALLSITE-FUNNEL-01/K3")
	assert.Empty(t, diags,
		"MQTT-ACK-CALLSITE-FUNNEL-01/K3: reflect.MethodByName(\"Ack\") detected in adapters/mqtt production code")
}

// scanMQTTReflectMethodByName walks production AST for `_.MethodByName(name)`
// calls where name ∈ methodNames (resolved via EvaluateConstString so a
// const-folded form is caught too).
func scanMQTTReflectMethodByName(t *testing.T, methodNames []string, ruleID string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !isMethodByNameCall(p.TypesInfo, call, methodNames) {
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: MethodByName(...) at %s:%d in adapters/mqtt production code — "+
								"reflect-based invocation of a funneled method is forbidden",
							ruleID, rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	return diags
}

// TestMQTTCallsiteFunnel_ReflectScannerNonVacuous proves isMethodByNameCall fires
// on a synthetic literal and a const-folded MethodByName arg for each funneled
// method name, and that the literal-only fallback does NOT match a const Ident.
func TestMQTTCallsiteFunnel_ReflectScannerNonVacuous(t *testing.T) {
	t.Parallel()
	const litSrc = `package x
import "reflect"
func f(v reflect.Value) {
	v.MethodByName("Publish")
	v.MethodByName("Subscribe")
	v.MethodByName("Unsubscribe")
	v.MethodByName("Ack")
}
`
	all := []string{publishMethodName, subscribeMethodName, unsubscribeMethodName, ackMethodName}
	fLit := mqttParseSnippet(t, litSrc)
	var litFired int
	EachInSubtree[ast.CallExpr](fLit, func(call *ast.CallExpr) {
		if isMethodByNameCall(nil, call, all) {
			litFired++
		}
	})
	assert.GreaterOrEqual(t, litFired, 4,
		"reflect MethodByName literal detector did not fire on the known literal calls")

	const constSrc = `package x
const m = "Ack"
type hasMethod interface{ MethodByName(string) any }
func f(v hasMethod) { v.MethodByName(m) }
`
	fConst, info := mqttTypeCheckSnippet(t, constSrc)
	var constFired, litOnly bool
	EachInSubtree[ast.CallExpr](fConst, func(call *ast.CallExpr) {
		if isMethodByNameCall(info, call, []string{ackMethodName}) {
			constFired = true
		}
		if isMethodByNameCall(nil, call, []string{ackMethodName}) {
			litOnly = true
		}
	})
	assert.True(t, constFired,
		"const-folded MethodByName(const) not detected with type info — the EvaluateConstString upgrade is broken")
	assert.False(t, litOnly,
		"sanity: the literal-only fallback must NOT match a const Ident arg")
}

// ─── A7: ConnectionManager alias blind-spot (shared by Publish + Subscribe) ───

// TestMQTTPublishCallsiteFunnel_A7_BlindSpot_NoConnectionManagerAlias bans any
// type alias of autopaho.ConnectionManager in adapters/mqtt. Because an alias
// would obscure ALL cm method funnels (Publish/Subscribe/Unsubscribe), this
// single guard covers the subscribe funnel's S7 blind-spot too — no duplicate.
func TestMQTTPublishCallsiteFunnel_A7_BlindSpot_NoConnectionManagerAlias(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A7"
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
					if ts.Assign == token.NoPos {
						return
					}
					tv, ok := p.TypesInfo.Types[ts.Type]
					if !ok {
						return
					}
					typ := tv.Type
					if ptr, isPtr := typ.(*types.Pointer); isPtr {
						typ = ptr.Elem()
					}
					named, ok := typ.(*types.Named)
					if !ok {
						return
					}
					tobj := named.Obj()
					if tobj.Pkg() == nil || tobj.Pkg().Path() != autophoPkgPath ||
						tobj.Name() != connectionManagerTypeName {
						return
					}
					pos := p.Fset.Position(ts.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: type alias `type %s = autopaho.ConnectionManager` (or pointer) at %s:%d "+
								"in adapters/mqtt is forbidden — aliases obscure the cm method funnels",
							ruleID, ts.Name.Name, rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A7: autopaho.ConnectionManager alias detected in adapters/mqtt production code")
}

// TestMQTTPublishCallsiteFunnel_A7_ScannerNonVacuous proves the alias-form
// detection (TypeSpec.Assign != NoPos) fires on a synthetic alias.
func TestMQTTPublishCallsiteFunnel_A7_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	const src = `package x
type CM = int
`
	f := mqttParseSnippet(t, src)
	var aliasForms int
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Assign != token.NoPos {
			aliasForms++
		}
	})
	assert.GreaterOrEqual(t, aliasForms, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A7: alias-form detection found 0 aliases in a snippet "+
			"that declares one — the AST traversal is broken (A7 would pass vacuously)")
}

// ─── Shared predicates / helpers ──────────────────────────────────────────────

// isCMReceiver reports whether expr has type *autopaho.ConnectionManager or
// autopaho.ConnectionManager in the given types.Info.
func isCMReceiver(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	typ := tv.Type
	if ptr, isPtr := typ.(*types.Pointer); isPtr {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil &&
		tobj.Pkg().Path() == autophoPkgPath &&
		tobj.Name() == connectionManagerTypeName
}

// isMethodExpressionCMCall reports whether call has the method-expression form
// (*autopaho.ConnectionManager).<method>(receiver, args...) with method ∈
// methodNames. Pure AST check (conservative over-approximation by selector name).
func isMethodExpressionCMCall(call *ast.CallExpr, methodNames []string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || !mqttInStringSet(sel.Sel.Name, methodNames) {
		return false
	}
	x := sel.X
	if paren, isParen := x.(*ast.ParenExpr); isParen {
		x = paren.X
	}
	if star, isStar := x.(*ast.StarExpr); isStar {
		return isCMSelectorExpr(star.X)
	}
	return isCMSelectorExpr(x)
}

// isCMSelectorExpr reports whether expr is a SelectorExpr pkg.ConnectionManager.
func isCMSelectorExpr(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == connectionManagerTypeName
}

// isMethodByNameCall reports whether call is `_.MethodByName(name)` with name ∈
// methodNames. The first argument is resolved via EvaluateConstString when type
// info is available, so a const-folded form is caught too; with nil info it
// falls back to a bare string-literal check.
func isMethodByNameCall(info *types.Info, call *ast.CallExpr, methodNames []string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "MethodByName" {
		return false
	}
	if len(call.Args) < 1 {
		return false
	}
	if info != nil {
		if val, ok := EvaluateConstString(info, call.Args[0]); ok {
			return mqttInStringSet(val, methodNames)
		}
	}
	lit, isLit := call.Args[0].(*ast.BasicLit)
	if !isLit {
		return false
	}
	val, ok := StringLitValue(lit)
	return ok && mqttInStringSet(val, methodNames)
}

// mqttParseSnippet parses an in-memory Go source string into an *ast.File for
// pure-AST detector non-vacuity proofs (no type checking).
func mqttParseSnippet(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "snippet.go", src, 0)
	require.NoError(t, err, "parse in-memory snippet for non-vacuous self-check")
	return f
}

// mqttTypeCheckSnippet parses + type-checks an in-memory Go source string and
// returns the file plus populated *types.Info, for const-eval based non-vacuity
// proofs. The snippet MUST NOT import any package (no Importer is configured).
func mqttTypeCheckSnippet(t *testing.T, src string) (*ast.File, *types.Info) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	require.NoError(t, err, "parse in-memory snippet for type-checked self-check")
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{} // no Importer — snippet must be import-free
	_, err = conf.Check("x", fset, []*ast.File{f}, info)
	require.NoError(t, err, "type-check in-memory snippet (must have no external imports)")
	return f, info
}

// ─── Compile-time import guards ───────────────────────────────────────────────

// A3-sibling reflect sanity: the exported sealed structs keep their {value string}
// shape (publishableTopic / subscribableFilter are unexported and checked via
// go/types in the field-freeze tests above). This keeps the mqtt import live.
func TestMQTTCallsiteFunnel_ExportedTypesShape(t *testing.T) {
	t.Parallel()
	assertMQTTSealedSingleValueField(t, "TopicNamespace", reflect.TypeOf(mqtt.TopicNamespace{}))
	assertMQTTSealedSingleValueField(t, "ClientID", reflect.TypeOf(mqtt.ClientID{}))
}

// pahoPkgPath is referenced via the K2 callee key; keep a compile-time use so a
// future rename of the constant is caught.
var _ = pahoPkgPath

var (
	_ = assert.Empty
	_ = require.True
)
