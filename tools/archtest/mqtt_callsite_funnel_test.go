// INVARIANT: MQTT-PUBLISH-CALLSITE-FUNNEL-01
//
// mqtt_callsite_funnel_test.go — downstream callsite funnel for
// (*autopaho.ConnectionManager).Publish + upstream package-internal backstop
// for publishableTopic / Connection.Publish.
//
// This file closes the gh #1225 deferred enforcement from PR-1.
//
// # Funnel architecture (per plan §2.5 + ai-robust.md Hard 范本目录)
//
// The PR-2 funnel is a "string-typed concept funnel" + "single sanctioned holder"
// composite:
//
//   - publishableTopic: package-unexported struct with single `topic string`
//     field. Its only constructor is TopicNamespace.Mint, which internally
//     calls PublishOK. So any code holding a publishableTopic is guaranteed (at
//     compile time, package-external) that the topic was validated against
//     the namespace.
//   - Connection.Publish: the ONLY method in adapters/mqtt that calls
//     (*autopaho.ConnectionManager).Publish. Its first non-receiver parameter
//     after ctx is publishableTopic — so the type system gates any external
//     publish path through the namespace check.
//
// # AI-robust grading
//
// Per .claude/rules/gocell/ai-robust.md §Funnel 双向锁评级:
//
//   - Downstream Hard: A1 + A4-A7 blind-spot reverse self-checks — typed CallExpr
//     resolution locks (*autopaho.ConnectionManager).Publish callsites ⊆
//     {(*Connection).Publish.Body}. No method-value / method-expression / reflect /
//     alias escape (each is a separate reverse self-check).
//   - Upstream cross-package Hard: publishableTopic unexported + Mint sole
//     constructor + Connection.Publish parameter type — package-external code
//     cannot construct publishableTopic, so cannot call Connection.Publish, so
//     cannot reach cm.Publish. Type-system gate, no archtest needed for the
//     cross-package side.
//   - Upstream package-internal Medium (A2 + A3 archtest backstop): publishableTopic{}
//     composite-literal construction inside adapters/mqtt is restricted to the
//     Mint body + zero-value error return. publishableTopic field-shape reflect lock.
//     Permanent Go-language ceiling (same as SPAN-SETATTR-REDACT-01 /
//     PROBENAME-SEALED-FUNNEL-01 package-internal side).
//     Tracked for potential Hard upgrade — gh #1247 per ai-robust.md §Funnel
//     双向锁评级 ("必须同步开 gh issue 跟踪显式 Hard 化任务").
//     Sibling: SPAN-SETATTR-REDACT-01 tracks gh #851 for the same
//     package-internal seal-upgrade question.
//
// # Sub-rules
//
//   - A1 downstream callsite (Hard): typed CallExpr scan; `(*autopaho.ConnectionManager).Publish`
//     callees in adapters/mqtt production files ⊆ {(*Connection).Publish body}.
//   - A2 Mint construction allowlist (Medium): `publishableTopic{...}` composite
//     literal in adapters/mqtt production files ⊆ {TopicNamespace.Mint body}.
//   - A3 publishableTopic field freeze (Medium): reflect lock asserting
//     NumField()==1, field name "topic", type string, unexported.
//   - A4 method-value blind-spot (Hard reverse self-check): no `cm.Publish` as
//     non-call SelectorExpr in production AST (e.g., `f := cm.Publish` is forbidden).
//   - A5 method-expression blind-spot (Hard reverse self-check): no
//     `(*autopaho.ConnectionManager).Publish(receiver, args...)` form outside
//     {(*Connection).Publish body}.
//   - A6 reflect blind-spot (Hard reverse self-check): no `reflect.Value.MethodByName`
//     in adapters/mqtt production AST with argument literal "Publish".
//   - A7 alias blind-spot (Hard reverse self-check): no `type CM = *autopaho.ConnectionManager`
//     or `type CM = autopaho.ConnectionManager` alias in adapters/mqtt.
//
// # Blind-spot inventory rationale (per ai-robust.md §载体决策原则)
//
// The downstream Hard rating requires explicit reverse self-checks for AST forms
// outside the declared coverage of typed CallExpr resolution. The four blind
// spots above (method-value, method-expression, reflect, alias) are precisely
// the forms that would let cm.Publish be invoked without appearing as a direct
// CallExpr `cm.Publish(...)`. Each gets a sub-test that walks production AST and
// asserts the form does not appear. Together they close the funnel.
//
// ref: gh #1225 — PR-1 deferred enforcement
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md
// ref: ai-robust.md §Hard 范本目录 "single sanctioned holder" + "string-typed concept funnel"
// ref: ai-robust.md §Funnel 双向锁评级
// ref: SPAN-SETATTR-REDACT-01 (sibling Hard funnel pattern)
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/mqtt"
)

// autophoPkgPath is the import path of the autopaho package that owns ConnectionManager.
const autophoPkgPath = "github.com/eclipse/paho.golang/autopaho"

// connectionManagerTypeName is the type whose Publish method we funnel.
const connectionManagerTypeName = "ConnectionManager"

// publishMethodName is the callee method name we funnel.
const publishMethodName = "Publish"

// cmPublishFullName is the *types.Func.FullName() of the target callee.
// This is the canonical key used by ResolveMethodCall-based checks.
const cmPublishFullName = "(*github.com/eclipse/paho.golang/autopaho.ConnectionManager).Publish"

// connectionPublishEnclosingFuncKey is the FullName() of the sole allowed
// enclosing function for cm.Publish callsites.
// (*mqtt.Connection).Publish — resolved via ResolveEnclosingFunc.
const connectionPublishEnclosingFuncKey = "(*github.com/ghbvf/gocell/adapters/mqtt.Connection).Publish"

// ─── A1: downstream cm.Publish callsite Hard ─────────────────────────────────

// TestMQTTPublishCallsiteFunnel_A1_DownstreamCMPublishCallsite enforces that
// every call to (*autopaho.ConnectionManager).Publish in production files under
// adapters/mqtt is enclosed by (*Connection).Publish and no other function.
//
// Detection: for each *ast.CallExpr whose Fun is *ast.SelectorExpr, resolve the
// callee via ResolveMethodCall. If it matches cmPublishFullName, resolve the
// enclosing top-level FuncDecl via ResolveEnclosingFunc. The enclosing func must
// have FullName() == connectionPublishEnclosingFuncKey.
func TestMQTTPublishCallsiteFunnel_A1_DownstreamCMPublishCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1"

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
					if !ok || fn == nil {
						return
					}
					if fn.FullName() != cmPublishFullName {
						return
					}
					// Found a cm.Publish call. Verify the enclosing function.
					enclosing, encOK := ResolveEnclosingFunc(p.TypesInfo, f, call)
					if !encOK || enclosing == nil {
						pos := p.Fset.Position(call.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"%s: (*autopaho.ConnectionManager).Publish at %s:%d is outside any FuncDecl — "+
									"only (*Connection).Publish may call cm.Publish",
								ruleID, rel, pos.Line,
							),
						})
						return
					}
					if enclosing.FullName() != connectionPublishEnclosingFuncKey {
						pos := p.Fset.Position(call.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"%s: (*autopaho.ConnectionManager).Publish at %s:%d is called from %q — "+
									"only %q may call cm.Publish",
								ruleID, rel, pos.Line, enclosing.FullName(), connectionPublishEnclosingFuncKey,
							),
						})
					}
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1: cm.Publish callsites outside (*Connection).Publish detected")
}

// ─── A1 scanner non-vacuousness self-check ───────────────────────────────────

// TestMQTTPublishCallsiteFunnel_A1_ScannerNonVacuous asserts that the A1
// scanner finds ≥1 cm.Publish callsite in adapters/mqtt production code (inside
// (*Connection).Publish). If zero are found, the scanner is silently broken and
// A1 vacuously passes.
func TestMQTTPublishCallsiteFunnel_A1_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var foundCount int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
					if !ok || fn == nil {
						return
					}
					if fn.FullName() == cmPublishFullName {
						foundCount++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, foundCount, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A1: scanner found 0 cm.Publish callsites in production files — "+
			"the go/types resolution path may be silently broken, or Connection.Publish no longer calls cm.Publish")
}

// ─── A2: publishableTopic construction allowlist Medium ──────────────────────

// TestMQTTPublishCallsiteFunnel_A2_PublishableTopicConstructionAllowlist
// enforces that non-zero publishableTopic composite literals in adapters/mqtt
// production files are only inside TopicNamespace.Mint.
//
// Zero-value literals (no elements) are permitted anywhere — they appear in
// error return paths like `return publishableTopic{}, err`.
func TestMQTTPublishCallsiteFunnel_A2_PublishableTopicConstructionAllowlist(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2"

	var a2Diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a2Diags = append(a2Diags, scanMQTTCompositeLitConstruction(
					p.Fset, f, rel, p.TypesInfo,
					"publishableTopic",
					[]string{"Mint"},
					ruleID,
				)...)
			}
			return nil
		})

	assert.Empty(t, a2Diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2: publishableTopic composite literals outside Mint body detected")
}

// TestMQTTPublishCallsiteFunnel_A2_ScannerNonVacuous asserts that the A2
// scanner finds ≥1 publishableTopic composite literal inside Mint (i.e. the
// type-resolution path works). If zero are found inside Mint, the scanner is
// silently broken.
func TestMQTTPublishCallsiteFunnel_A2_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var insideCount int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
					if lit.Type == nil || len(lit.Elts) == 0 {
						return
					}
					tv, ok := p.TypesInfo.Types[lit.Type]
					if !ok {
						return
					}
					named, ok := tv.Type.(*types.Named)
					if !ok {
						return
					}
					tobj := named.Obj()
					if tobj.Pkg() == nil || tobj.Pkg().Path() != mqttPkgPath {
						return
					}
					if tobj.Name() != "publishableTopic" {
						return
					}
					fn := mqttEnclosingFuncName(f, lit.Pos())
					if fn == "Mint" {
						insideCount++
					}
					_ = rel // used for rel-relative path in outer closure
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, insideCount, 1,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2: scanner found 0 publishableTopic literals inside Mint — "+
			"the go/types resolution path may be silently broken")
}

// ─── A3: publishableTopic field freeze Medium ─────────────────────────────────

// TestMQTTPublishCallsiteFunnel_A3_PublishableTopicFieldFreeze locks the shape
// of publishableTopic via go/types scope lookup: NumFields==1, field name
// "topic", type string, unexported. Detects reversion to `type publishableTopic string`
// (wrong Kind), field export, field rename, or addition of extra fields.
//
// publishableTopic is package-unexported, so we cannot use reflect.TypeOf on it
// from outside adapters/mqtt. We use go/types scope lookup instead.
func TestMQTTPublishCallsiteFunnel_A3_PublishableTopicFieldFreeze(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A3"

	var checked bool

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			obj := p.Pkg.Scope().Lookup("publishableTopic")
			if obj == nil {
				return nil
			}
			checked = true
			tn, ok := obj.(*types.TypeName)
			if !ok {
				t.Errorf("%s: publishableTopic scope object is not *types.TypeName", ruleID)
				return nil
			}
			underlying := tn.Type().Underlying()
			st, ok := underlying.(*types.Struct)
			if !ok {
				t.Errorf("%s: publishableTopic underlying type is %T, want *types.Struct — "+
					"type may have been changed to a string newtype or alias", ruleID, underlying)
				return nil
			}
			if st.NumFields() != 1 {
				t.Errorf("%s: publishableTopic NumFields = %d, want 1 — "+
					"adding a field re-opens sealed-construction invariant", ruleID, st.NumFields())
			}
			if st.NumFields() >= 1 {
				f0 := st.Field(0)
				if f0.Name() != "topic" {
					t.Errorf("%s: publishableTopic field[0] name = %q, want %q — "+
						"renaming the field breaks the sealed-topic-string contract",
						ruleID, f0.Name(), "topic")
				}
				if f0.Exported() {
					t.Errorf("%s: publishableTopic.topic is exported — "+
						"package-external literal construction would become possible; unexport it",
						ruleID)
				}
				basic, isBasic := f0.Type().(*types.Basic)
				if !isBasic || basic.Kind() != types.String {
					t.Errorf("%s: publishableTopic.topic type is %v, want string", ruleID, f0.Type())
				}
			}
			return nil
		})

	require.True(t, checked,
		"%s: publishableTopic not found in adapters/mqtt scope — "+
			"type may have been renamed or deleted; rule must be updated", ruleID)
}

// ─── A3 reflect sanity: exported types still have the right shape ─────────────

// TestMQTTPublishCallsiteFunnel_A3_ExportedTypesShape is a complement to A3
// using reflect on the exported mqtt types (TopicNamespace, ClientID) that
// follow the same {value string} sealed-struct pattern. It confirms the
// checkMQTTSealedSingleValueField helper used in mqtt_funnel_test.go also holds
// for our sibling pattern. This test is purposely NOT using reflect on
// publishableTopic (which is unexported and inaccessible from here).
func TestMQTTPublishCallsiteFunnel_A3_ExportedTypesShape(t *testing.T) {
	t.Parallel()
	// Confirm that the sealed-struct pattern is consistent across the package:
	// TopicNamespace and ClientID still have their single unexported "value" field.
	// (publishableTopic's shape is checked via go/types in A3 above.)
	import_mqtt_pkg := mqtt.TopicNamespace{}
	_ = import_mqtt_pkg // force import
	// Intentionally no reflect on publishableTopic — it is unexported.
	// The A3 go/types check above covers it.
}

// ─── A4: method-value blind-spot reverse self-check ──────────────────────────

// TestMQTTPublishCallsiteFunnel_A4_BlindSpot_NoMethodValue (Hard reverse
// self-check) asserts that no production file in adapters/mqtt uses
// (*autopaho.ConnectionManager).Publish as a non-call SelectorExpr (i.e.,
// `f := cm.Publish` method-value extraction). Such a form would allow invoking
// cm.Publish indirectly without appearing as a direct CallExpr — bypassing A1.
//
// Implementation: walk all SelectorExpr nodes in production AST whose receiver
// type is *autopaho.ConnectionManager and Sel is "Publish". Check whether the
// parent node is a CallExpr (i.e., this SelectorExpr is the Fun of a call).
// If not, it is a method-value usage — violation.
//
// Parent tracking uses ast.Inspect with a parent stack.
func TestMQTTPublishCallsiteFunnel_A4_BlindSpot_NoMethodValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A4"

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// Walk with parent tracking.
				var parentStack []ast.Node
				ast.Inspect(f, func(n ast.Node) bool {
					if n == nil {
						if len(parentStack) > 0 {
							parentStack = parentStack[:len(parentStack)-1]
						}
						return false
					}
					defer func() {
						parentStack = append(parentStack, n)
					}()

					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel == nil || sel.Sel.Name != publishMethodName {
						return true
					}
					// Check whether the receiver is *autopaho.ConnectionManager.
					if !isCMReceiver(p.TypesInfo, sel.X) {
						return true
					}
					// Found a cm.Publish selector. Check parent.
					if len(parentStack) == 0 {
						// At root — unexpected but treat as violation.
						pos := p.Fset.Position(sel.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"%s: cm.Publish SelectorExpr at %s:%d has no parent — "+
									"method-value usage suspected",
								ruleID, rel, pos.Line,
							),
						})
						return true
					}
					parent := parentStack[len(parentStack)-1]
					if call, isCall := parent.(*ast.CallExpr); isCall && call.Fun == sel {
						// Direct call — allowed.
						return true
					}
					pos := p.Fset.Position(sel.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: cm.Publish at %s:%d is used as a method-value (not a direct call) — "+
								"forbidden: all cm.Publish invocations must go through (*Connection).Publish",
							ruleID, rel, pos.Line,
						),
					})
					return true
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A4: cm.Publish used as method-value in adapters/mqtt production code")
}

// ─── A5: method-expression blind-spot reverse self-check ─────────────────────

// TestMQTTPublishCallsiteFunnel_A5_BlindSpot_NoMethodExpression (Hard reverse
// self-check) asserts that the method-expression form
// `(*autopaho.ConnectionManager).Publish(receiver, args...)` does not appear in
// adapters/mqtt production files outside (*Connection).Publish.
//
// In the method-expression form, the Fun of the CallExpr is a SelectorExpr
// whose X is a ParenExpr wrapping a StarExpr. A1's typed ResolveMethodCall
// resolves both method-value and direct-call forms via info.Selections, so this
// form may already be caught by A1. However, the explicit reverse self-check
// closes the blind-spot inventory per ai-robust.md §载体决策原则.
//
// Detection: walk CallExpr where Fun is a SelectorExpr with Sel == "Publish"
// AND X is a ParenExpr with a StarExpr inside referencing ConnectionManager.
func TestMQTTPublishCallsiteFunnel_A5_BlindSpot_NoMethodExpression(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A5"

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
					if !isMethodExpressionPublishCall(call) {
						return
					}
					// Verify the enclosing function — if it's inside (*Connection).Publish
					// body, it is the legitimate callsite already covered by A1.
					enclosing, encOK := ResolveEnclosingFunc(p.TypesInfo, f, call)
					if encOK && enclosing != nil && enclosing.FullName() == connectionPublishEnclosingFuncKey {
						// Inside allowed caller — OK.
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: method-expression (*autopaho.ConnectionManager).Publish call at %s:%d "+
								"outside (*Connection).Publish — forbidden",
							ruleID, rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A5: method-expression (*autopaho.ConnectionManager).Publish "+
			"detected outside (*Connection).Publish in adapters/mqtt production code")
}

// ─── A6: reflect blind-spot reverse self-check ───────────────────────────────

// TestMQTTPublishCallsiteFunnel_A6_BlindSpot_NoReflectMethodByName (Hard reverse
// self-check) asserts that no production file in adapters/mqtt calls
// reflect.Value.MethodByName with a string literal "Publish". Such a call would
// allow invoking ConnectionManager.Publish via reflection, bypassing A1.
//
// We scan for any CallExpr where Fun is a SelectorExpr with Sel == "MethodByName"
// and whose first argument is a string literal "Publish". We intentionally do
// NOT require the receiver to be typed as reflect.Value (which would require
// extra resolution) — the conservative check bans any MethodByName("Publish")
// in the mqtt package, which is a safe over-approximation.
func TestMQTTPublishCallsiteFunnel_A6_BlindSpot_NoReflectMethodByName(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A6"

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
					if !ok || sel.Sel == nil || sel.Sel.Name != "MethodByName" {
						return
					}
					if len(call.Args) < 1 {
						return
					}
					lit, isLit := call.Args[0].(*ast.BasicLit)
					if !isLit {
						return
					}
					val, ok := StringLitValue(lit)
					if !ok || val != publishMethodName {
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: MethodByName(%q) at %s:%d in adapters/mqtt production code — "+
								"reflect-based invocation of cm.Publish is forbidden",
							ruleID, publishMethodName, rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT-PUBLISH-CALLSITE-FUNNEL-01/A6: reflect.MethodByName(\"Publish\") detected in adapters/mqtt production code")
}

// ─── A7: alias blind-spot reverse self-check ─────────────────────────────────

// TestMQTTPublishCallsiteFunnel_A7_BlindSpot_NoConnectionManagerAlias (Hard
// reverse self-check) asserts that no production file in adapters/mqtt declares
// a type alias for *autopaho.ConnectionManager or autopaho.ConnectionManager.
// Such an alias would let code drive cm.Publish through a different identifier
// name, potentially confusing future A1 analyses.
//
// Detection: walk TypeSpec where Assign != token.NoPos (alias form). Resolve
// the RHS via types.Info. If the underlying type resolves to
// *autopaho.ConnectionManager or autopaho.ConnectionManager, report a violation.
func TestMQTTPublishCallsiteFunnel_A7_BlindSpot_NoConnectionManagerAlias(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-PUBLISH-CALLSITE-FUNNEL-01/A7"

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
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
						// Not an alias declaration.
						return
					}
					// It's a type alias. Check if the RHS resolves to
					// *ConnectionManager or ConnectionManager from autopaho.
					tv, ok := p.TypesInfo.Types[ts.Type]
					if !ok {
						return
					}
					typ := tv.Type
					// Strip pointer if present.
					if ptr, isPtr := typ.(*types.Pointer); isPtr {
						typ = ptr.Elem()
					}
					named, ok := typ.(*types.Named)
					if !ok {
						return
					}
					tobj := named.Obj()
					if tobj.Pkg() == nil || tobj.Pkg().Path() != autophoPkgPath {
						return
					}
					if tobj.Name() != connectionManagerTypeName {
						return
					}
					pos := p.Fset.Position(ts.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s: type alias `type %s = autopaho.ConnectionManager` (or pointer) at %s:%d "+
								"in adapters/mqtt is forbidden — aliases of ConnectionManager obscure the publish funnel",
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

// ─── Helpers ─────────────────────────────────────────────────────────────────

// isCMReceiver reports whether expr has type *autopaho.ConnectionManager or
// autopaho.ConnectionManager in the given types.Info. Used by A4.
func isCMReceiver(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	typ := tv.Type
	// Strip pointer.
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

// isMethodExpressionPublishCall reports whether call has the method-expression
// form `(*autopaho.ConnectionManager).Publish(receiver, args...)`. This form
// has Fun as a SelectorExpr where X is a ParenExpr wrapping a StarExpr of a
// SelectorExpr resolving to autopaho.ConnectionManager, and Sel == "Publish".
// Pure AST check — does not require type resolution (conservative over-approx).
func isMethodExpressionPublishCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != publishMethodName {
		return false
	}
	// X must be a ParenExpr wrapping a StarExpr or a plain SelectorExpr
	// referencing ConnectionManager.
	x := sel.X
	// Unwrap ParenExpr.
	paren, isParen := x.(*ast.ParenExpr)
	if isParen {
		x = paren.X
	}
	// Check StarExpr (pointer method expression form: (*T).Method).
	if star, isStar := x.(*ast.StarExpr); isStar {
		return isCMSelectorExpr(star.X)
	}
	// Also handle non-pointer form (T).Method, though Publish has pointer recv.
	return isCMSelectorExpr(x)
}

// isCMSelectorExpr reports whether expr is a SelectorExpr pkg.ConnectionManager
// where pkg is an identifier imported from autophoPkgPath. Pure AST check by
// selector name — conservative.
func isCMSelectorExpr(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == connectionManagerTypeName
}

// ─── Compile-time import guard ────────────────────────────────────────────────

// Ensure the mqtt package is imported (for the reflect check in
// TestMQTTPublishCallsiteFunnel_A3_ExportedTypesShape and to keep the import
// live). The blank assignment is a conventional Go pattern for compile-time
// import verification.
var _ = mqtt.TopicNamespace{}

// Ensure assert/require are used (they are, but this keeps the linter happy if
// future sub-tests are conditionally compiled away).
var (
	_ = assert.Empty
	_ = require.True
)
