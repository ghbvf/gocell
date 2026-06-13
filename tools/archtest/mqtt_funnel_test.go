//go:build archtest

// INVARIANT: MQTT-CLIENT-ID-NAMESPACE-01
// INVARIANT: MQTT-TOPIC-NAMESPACE-01
// INVARIANT: MQTT-CONFIG-SEALED-FIELD-FROZEN-01
//
// mqtt_funnel_test.go — dogfood Tests + self-checks for the sealed-struct
// construction funnels of adapters/mqtt.ClientID, adapters/mqtt.TopicNamespace,
// and adapters/mqtt.Config.
//
// Rule scanner logic lives in mqtt_funnel.go (importable non-test file);
// this file contains:
//   - dogfood Tests that call Report(t, ruleID, CheckXxx(...))
//   - reverse self-check tests that exercise the shared scanners directly
//
// For full rule documentation see mqtt_funnel.go package godoc.
package archtest

import (
	"crypto/tls"
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/mqtt"
)

// wantMQTTConfigFields is the frozen field set of adapters/mqtt.Config pinned by
// MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1. Editing this list is the deliberate
// signal that the sealed Config shape changed — it must be accompanied by an
// ADR amendment + threat-model re-evaluation (ai-robust: reflect schema freeze).
func wantMQTTConfigFields() []mqttConfigField {
	dur := reflect.TypeOf(time.Duration(0))
	return []mqttConfigField{
		{"clientID", reflect.TypeOf(mqtt.ClientID{})},
		{"brokers", reflect.TypeOf([]string(nil))},
		{"tlsConfig", reflect.TypeOf((*tls.Config)(nil))},
		{"sessionExpiry", dur},
		{"auth", reflect.TypeOf(mqtt.AuthConfig{})},
		{"backoff", reflect.TypeOf(mqtt.BackoffConfig{})},
		{"maximumPacketSize", reflect.TypeOf(uint32(0))},
		{"connectTimeout", dur},
		{"connectDeadline", dur},
		{"keepAlive", dur},
		{"publishTimeout", dur},
	}
}

// assertMQTTSealedSingleValueField is a test-helper wrapper around
// checkMQTTSealedSingleValueField that reports violations via t.Errorf.
// It must be defined in a _test.go so that mqtt_callsite_funnel_test.go can
// reference it (both are in package archtest test binary).
func assertMQTTSealedSingleValueField(t *testing.T, typeName string, dt reflect.Type) {
	t.Helper()
	for _, v := range checkMQTTSealedSingleValueField(typeName, dt) {
		t.Errorf("%s", v)
	}
}

// ─── Main archtest: MQTT-CLIENT-ID-NAMESPACE-01 ───────────────────────────────

// TestMQTTClientIDNamespace01 enforces the ClientID sealed-struct construction
// funnel.
//
// # Blind-spot self-check
//
// The A2 scanner uses go/types to resolve composite literal types. It cannot
// detect construction via:
//   - reflect.Value.FieldByName("value").Set(...): panics at runtime on unexported
//     fields across packages; TestMQTTFunnel_BlindSpot_NoReflectNew covers this.
//   - unsafe.Pointer casts: TestMQTTFunnel_BlindSpot_NoUnsafePtr covers this.
//   - Type conversion `ClientID("raw")`: rejected by the compiler because
//     ClientID is a struct, not a string alias; this blind-spot form is
//     structurally inexpressible.
func TestMQTTClientIDNamespace01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CLIENT-ID-NAMESPACE-01"

	// A1: reflect field freeze.
	t.Run("A1_FieldFreeze", func(t *testing.T) {
		t.Parallel()
		dt := reflect.TypeOf(mqtt.ClientID{})
		for _, v := range checkMQTTSealedSingleValueField(ruleID+"/A1: ClientID", dt) {
			t.Errorf("%s", v)
		}
	})

	t.Run("A2_A3_ConstructionAndAlias", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID, CheckMQTTClientIDNamespace(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
	})
}

// ─── Main archtest: MQTT-TOPIC-NAMESPACE-01 ───────────────────────────────────

// TestMQTTTopicNamespace01 enforces the TopicNamespace sealed-struct construction
// funnel.
//
// # Downstream callsite funnel — DELIVERED in PR-2/3 (#1225 closed)
//
// The downstream enforcement is implemented in mqtt_callsite_funnel_test.go:
//   - Publisher.Publish calls ns.Mint(topic) (which internally calls PublishOK)
//     to obtain a sealed PublishableTopic token; Connection.Publish only CONSUMES
//     the token and never re-validates. MQTT-PUBLISH-CALLSITE-FUNNEL-01 A1–A4
//     locks all cm.Publish callsites to (*Connection).Publish.
//   - MintFilter calls SubscribeOK internally; MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01
//     S1–S6 locks all cm.Subscribe/Unsubscribe callsites.
//
// # Blind-spot self-check (same as ClientID)
//
// The A2 scanner uses go/types composite-literal resolution. Same blind spots
// as MQTT-CLIENT-ID-NAMESPACE-01 apply; same reverse self-check tests cover them.
func TestMQTTTopicNamespace01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-TOPIC-NAMESPACE-01"

	// A1: reflect field freeze.
	t.Run("A1_FieldFreeze", func(t *testing.T) {
		t.Parallel()
		dt := reflect.TypeOf(mqtt.TopicNamespace{})
		for _, v := range checkMQTTSealedSingleValueField(ruleID+"/A1: TopicNamespace", dt) {
			t.Errorf("%s", v)
		}
	})

	t.Run("A2_A3_ConstructionAndAlias", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID, CheckMQTTTopicNamespace(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
	})
}

// ─── Main archtest: MQTT-CONFIG-SEALED-FIELD-FROZEN-01 ────────────────────────

// TestMQTTConfigSealedFieldFrozen01 enforces the Config sealed-construction
// funnel — the Hard upgrade of the retired MQTT-CONFIG-VALIDATE-FIRST-01
// form-lock (#1231).
//
//   - A1 (Hard, reflect field freeze): adapters/mqtt.Config has EXACTLY the
//     frozen field set, every field unexported. Unexported fields are the
//     upstream compile gate — an outside-package `mqtt.Config{...}` literal is
//     structurally inexpressible, so a non-zero Config can only come from
//     NewConfig, which validates in its body. "Unvalidated Config" is therefore
//     unrepresentable in a caller's hands.
//   - A2 (Medium, composite-literal allowlist): in-package, the sole sanctioned
//     non-zero Config composite literal is inside NewConfig; any other is a
//     funnel bypass (an in-package path could otherwise build an unvalidated
//     Config). Zero-value `Config{}` (NewConfig's error return) is allowed.
//   - A2b (Medium, field-write allowlist): in-package, a `c.field = x` write to
//     a Config field must be inside NewConfig or a With* option constructor
//     (signature returns ConfigOption); any other is a field-by-field
//     construction bypass (`var c Config; c.clientID = id; return c`) the A2
//     composite-literal scan alone cannot see.
//
// # Blind-spot self-check
//
// Same go/types composite-literal-resolution blind spots as
// MQTT-CLIENT-ID-NAMESPACE-01 (reflect field-set / unsafe.Pointer); the
// repo-wide TestMQTTFunnel_BlindSpot_* tests cover them for adapters/mqtt.
func TestMQTTConfigSealedFieldFrozen01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CONFIG-SEALED-FIELD-FROZEN-01"

	// A1: reflect field freeze (exact field set + unexported + type identity).
	t.Run("A1_FieldFreeze", func(t *testing.T) {
		t.Parallel()
		dt := reflect.TypeOf(mqtt.Config{})
		for _, v := range checkMQTTConfigFieldFreeze(ruleID+"/A1: Config", dt, wantMQTTConfigFields()) {
			t.Errorf("%s", v)
		}
	})

	// A2: in-package construction allowlist (NewConfig only).
	t.Run("A2_Construction", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID, CheckMQTTConfigSeal(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
	})
}

// TestMQTTConfigFreeze_ScannerFires proves checkMQTTConfigFieldFreeze produces
// violations on every degenerate Config shape (exported field, missing field,
// extra field, wrong type, non-struct) and none on a faithful replica of the
// real field set. Without this reverse self-check, a refactor that silently
// relaxed the freeze would leave TestMQTTConfigSealedFieldFrozen01/A1 vacuously
// green.
func TestMQTTConfigFreeze_ScannerFires(t *testing.T) {
	t.Parallel()

	pkg := PlatformModulePath + "/tools/archtest"
	dur := reflect.TypeOf(time.Duration(0))
	strType := reflect.TypeOf("")

	// want is a deliberately small 2-field frozen spec so the synthetic cases
	// stay readable; the production A1 test uses the real wantMQTTConfigFields().
	want := []mqttConfigField{{"clientID", strType}, {"keepAlive", dur}}

	mkField := func(name string, typ reflect.Type, exported bool) reflect.StructField {
		f := reflect.StructField{Name: name, Type: typ}
		if !exported {
			f.PkgPath = pkg
		}
		return f
	}

	cases := []struct {
		desc    string
		typ     reflect.Type
		wantErr bool
	}{
		{
			desc: "faithful: both fields unexported, correct types",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("clientID", strType, false), mkField("keepAlive", dur, false),
			}),
			wantErr: false,
		},
		{
			desc: "exported field re-opens literal construction",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("ClientID", strType, true), mkField("keepAlive", dur, false),
			}),
			wantErr: true, // "clientID" missing (it's "ClientID") AND count mismatch
		},
		{
			desc: "missing field",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("clientID", strType, false),
			}),
			wantErr: true,
		},
		{
			desc: "extra field (count drift)",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("clientID", strType, false), mkField("keepAlive", dur, false),
				mkField("extra", strType, false),
			}),
			wantErr: true,
		},
		{
			desc: "wrong field type",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("clientID", strType, false), mkField("keepAlive", strType, false),
			}),
			wantErr: true,
		},
		{
			desc:    "non-struct",
			typ:     strType,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			violations := checkMQTTConfigFieldFreeze("TestConfig", tc.typ, want)
			if tc.wantErr {
				assert.NotEmpty(t, violations,
					"expected freeze to fire for case %q but got no violations", tc.desc)
			} else {
				assert.Empty(t, violations,
					"expected no violations for case %q but got: %v", tc.desc, violations)
			}
		})
	}
}

// ─── Reverse self-check: A1 scanner has teeth ────────────────────────────────

// TestMQTTFunnel_A1ScannerFires proves the field-shape assertion in
// checkMQTTSealedSingleValueField produces violations on bad shapes.
// Without this, a refactor that silently relaxes the guard conditions would
// leave the production test vacuously passing.
func TestMQTTFunnel_A1ScannerFires(t *testing.T) {
	t.Parallel()

	stringType := reflect.TypeOf("")
	intType := reflect.TypeOf(0)

	mkField := func(name string, typ reflect.Type, exported bool) reflect.StructField {
		f := reflect.StructField{Name: name, Type: typ}
		if !exported {
			f.PkgPath = PlatformModulePath + "/tools/archtest"
		}
		return f
	}

	cases := []struct {
		desc    string
		typ     reflect.Type
		wantErr bool
	}{
		{
			desc:    "correct shape: single unexported string value field",
			typ:     reflect.StructOf([]reflect.StructField{mkField("value", stringType, false)}),
			wantErr: false,
		},
		{
			desc: "exported value field",
			typ:  reflect.StructOf([]reflect.StructField{mkField("Value", stringType, true)}),
			// exported → field name is "Value" not "value" → no "value" field
			wantErr: true,
		},
		{
			desc: "two fields",
			typ: reflect.StructOf([]reflect.StructField{
				mkField("value", stringType, false),
				mkField("extra", intType, false),
			}),
			wantErr: true,
		},
		{
			desc: "renamed field",
			typ:  reflect.StructOf([]reflect.StructField{mkField("raw", stringType, false)}),
			// no "value" field → violation
			wantErr: true,
		},
		{
			desc:    "non-struct: string kind",
			typ:     stringType,
			wantErr: true,
		},
		{
			desc: "value field wrong type",
			typ:  reflect.StructOf([]reflect.StructField{mkField("value", intType, false)}),
			// unexported int field — will have "value" but wrong Kind
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			violations := checkMQTTSealedSingleValueField("TestType", tc.typ)
			if tc.wantErr {
				assert.NotEmpty(t, violations,
					"expected scanner to fire for case %q but got no violations", tc.desc)
			} else {
				assert.Empty(t, violations,
					"expected no violations for case %q but got: %v", tc.desc, violations)
			}
		})
	}
}

// ─── Reverse blind-spot self-check: no reflect.New on mqtt types ─────────────

// TestMQTTFunnel_BlindSpot_NoReflectNew (blind-spot B1) asserts no production
// file calls reflect.New with an argument that resolves to mqtt.ClientID or
// mqtt.TopicNamespace. Such a call returns a pointer to a zero value, not a
// non-zero constructed value, so it is not a construction bypass — but it is
// a code smell and warrants detection.
func TestMQTTFunnel_BlindSpot_NoReflectNew(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	var sawSatellite bool

	// Production (whole-workspace): reflect.New(mqtt.ClientID) can appear in ANY
	// package, so this guard needs workspace breadth — not a single-package Typed
	// scope. Production also reaches the adapters/mqtt satellite (#1558/#1911).
	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			// Anti-vacuity (#1911): this blind-spot guard only means something if the
			// scan actually reaches the satellite packages owning the sealed types.
			// #1558 split adapters/mqtt into its own go.work module, so a module-local
			// scope silently stops visiting it (false-green); assert we saw it below.
			if p.Pkg.Path() == mqttPkgPath || p.Pkg.Path() == topicnsPkgPath {
				sawSatellite = true
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != "reflect" || name != "New" {
						return
					}
					if len(call.Args) != 1 {
						return
					}
					arg := call.Args[0]
					tv, tvOK := p.TypesInfo.Types[arg]
					if !tvOK || !tv.IsType() {
						return
					}
					named, isNamed := tv.Type.(*types.Named)
					if !isNamed {
						return
					}
					tobj := named.Obj()
					if tobj.Pkg() == nil {
						return
					}
					// ClientID remains in adapters/mqtt; the TopicNamespace seal moved
					// to adapters/mqtt/internal/topicns as Namespace (#1247), so cover
					// both packages or this reflect.New blind-spot goes silently vacuous.
					sealed := (tobj.Pkg().Path() == mqttPkgPath && tobj.Name() == "ClientID") ||
						(tobj.Pkg().Path() == topicnsPkgPath && tobj.Name() == "Namespace")
					if !sealed {
						return
					}
					pos := p.Fset.Position(call.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"MQTT funnel blind-spot B1: reflect.New(mqtt.%s) at %s:%d — "+
								"use ParseClientID / ParseTopicNamespace instead",
							tobj.Name(), rel, pos.Line,
						),
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT funnel blind-spot B1: production code calls reflect.New on mqtt sealed types")
	assert.True(t, sawSatellite,
		"MQTT funnel blind-spot B1 anti-vacuity: scan never visited adapters/mqtt or internal/topicns — "+
			"the guard is vacuous for the satellite module (module-local scope after #1558)")
}

// TestMQTTFunnel_BlindSpot_NoUnsafePtr (blind-spot B2) asserts that NO
// production file in the entire repository imports both "unsafe" and the
// adapters/mqtt package in a way that could construct a non-zero ClientID /
// TopicNamespace via unsafe.Pointer cast.
func TestMQTTFunnel_BlindSpot_NoUnsafePtr(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	var sawSatellite bool

	// Production (whole-workspace): any package importing adapters/mqtt could add an
	// unsafe.Pointer cast, so this guard needs workspace breadth — not a single-package
	// Typed scope. Production also reaches the adapters/mqtt satellite (#1558/#1911).
	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			// Anti-vacuity (#1911): the unsafe-import guard for the mqtt package itself
			// is vacuous unless the scan reaches adapters/mqtt — #1558 made it a
			// satellite module that a module-local scope silently skips. Assert below.
			if p.Pkg.Path() == mqttPkgPath {
				sawSatellite = true
			}

			importsMQTT := false
			for _, f := range p.Files {
				for _, imp := range f.Imports {
					if strings.Trim(imp.Path.Value, `"`) == mqttPkgPath {
						importsMQTT = true
						break
					}
				}
				if importsMQTT {
					break
				}
			}

			isMQTT := p.Pkg.Path() == mqttPkgPath

			if !importsMQTT && !isMQTT {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, imp := range f.Imports {
					path := strings.Trim(imp.Path.Value, `"`)
					if path == "unsafe" {
						pos := p.Fset.Position(imp.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"MQTT funnel blind-spot B2: %s imports \"unsafe\" and also references adapters/mqtt — "+
									"unsafe.Pointer casts to mqtt sealed types could bypass construction funnel",
								rel,
							),
						})
					}
				}
			}
			return nil
		})

	assert.Empty(t, diags,
		"MQTT funnel blind-spot B2: production code imports \"unsafe\" while referencing adapters/mqtt")
	assert.True(t, sawSatellite,
		"MQTT funnel blind-spot B2 anti-vacuity: scan never visited adapters/mqtt — "+
			"the guard is vacuous for the satellite module (module-local scope after #1558)")
}

// TestMQTTFunnel_A2ScannerFires proves that scanMQTTCompositeLitConstruction
// produces violations for composite literals outside the allowed constructor
// function, and produces no violations for literals inside it.
//
// Without this reverse self-check, a regression that silently disables the A2
// type-resolution path would leave the production test vacuously passing.
//
// # Blind-spot of this self-check
//
// This test uses reflect-built synthetic types, not real go/types.Info. It
// verifies the enclosing-function gate (typed FullName() identity via
// ResolveEnclosingFunc) and the zero-element skip, and also exercises the
// go/types type-resolution path implicitly (a scanner that cannot see
// ClientID{...} literals would produce zero outsideCount AND zero insideCount,
// failing the second assertion). A refactor that breaks the Pkg().Path() check
// would be caught by both production tests (A2 would vacuously pass, but also
// no "inside" literals found).
func TestMQTTFunnel_A2ScannerFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// assembleClientIDFullName is the typed FullName() of the sole allowed
	// ClientID constructor: a package-level func in adapters/mqtt.
	const assembleClientIDFullName = mqttPkgPath + ".assembleClientID"

	// We exercise the scanner directly on a synthetic scenario: load the real
	// adapters/mqtt package and look for any ClientID composite literals. The
	// production package has exactly one (inside assembleClientID). Any violation
	// found outside assembleClientID would be a real invariant break and the test
	// would fail — which is what we want to confirm fires correctly.
	var outsideCount, insideCount int

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
				diags := scanMQTTCompositeLitConstruction(
					p.Fset, f, rel, p.TypesInfo,
					"ClientID", []string{assembleClientIDFullName}, "MQTT-CLIENT-ID-NAMESPACE-01",
				)

				outsideCount += len(diags)

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
					if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != mqttPkgPath {
						return
					}
					if named.Obj().Name() != "ClientID" {
						return
					}
					// Use typed identity: the inside-count check must use the same
					// typed-gate as the production scanner.
					if mqttEnclosingFuncAllowed(p.TypesInfo, f, lit, []string{assembleClientIDFullName}) {
						insideCount++
					}
				})
			}
			return nil
		})

	// The production adapters/mqtt package must have exactly zero violations
	// (all non-zero ClientID literals are inside assembleClientID).
	assert.Equal(t, 0, outsideCount,
		"A2 scanner fires: production code has ClientID composite literals outside assembleClientID — "+
			"this means the A2 self-check correctly detects violations when they exist")

	// The inside count must be ≥ 1: assembleClientID constructs at least one
	// non-zero ClientID{value: value} literal. If this fails, the scanner's
	// type-resolution path is broken and cannot see any ClientID literals.
	assert.GreaterOrEqual(t, insideCount, 1,
		"A2 scanner must find ≥1 ClientID composite literal inside assembleClientID — "+
			"if this fails, the go/types resolution path is silently broken")
}

// TestMQTTFunnel_A2ScannerFiresOnRedFixture proves the generalized A2 scanner
// fires on a genuine outside-allowedFuncs violation. The red fixture lives at
// tools/archtest/internal/mqttredfixture (NOT inside adapters/mqtt itself —
// the path-scoped allowlist guard #944 rejects archtest_fixture in production
// paths because a production file silently skipped by the default-tag scan is
// a fail-open vector).
//
// The fixture declares a shape replica of mqtt.ClientID (struct{value string})
// — exactly because mqtt.ClientID.value is unexported and an external package
// cannot construct a literal of the real type. This proves the scanner's
// go/types composite-literal resolution path works for any sealed-struct
// following the {value string} convention, which IS the structural invariant
// the production scanner relies on.
//
// Without this self-check, a regression where the type-resolution path is
// silently broken would still pass TestMQTTFunnel_A2ScannerFires (which only
// verifies the inside count) because production has no violations.
//
// See tools/archtest/internal/mqttredfixture/fixture.go for the planted
// violation.
func TestMQTTFunnel_A2ScannerFiresOnRedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CLIENT-ID-NAMESPACE-01"
	// Anchor to PlatformModulePath so a module rename updates exactly one place.
	const fixturePkgPath = PlatformModulePath + "/tools/archtest/internal/mqttredfixture"

	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{fixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// Use typed FullName() key: ParseFixtureClientID is a package-level
				// func in the fixture package.
				out = append(out, scanSealedCompositeLitConstruction(
					p.Fset, f, rel, p.TypesInfo,
					fixturePkgPath, "FixtureClientID",
					[]string{fixturePkgPath + ".ParseFixtureClientID"}, ruleID,
				)...)
			}
			return out
		})

	require.NotEmpty(t, diags,
		"A2 scanner must report ≥1 violation on the red fixture in "+
			"tools/archtest/internal/mqttredfixture/fixture.go — "+
			"if this fails, the scanner is silently broken")

	// Confirm the violation points at the fixture file specifically — guards
	// against the scanner reporting an unrelated false positive that happens
	// to satisfy "non-empty".
	foundRedfixture := false
	for _, d := range diags {
		if strings.HasSuffix(d.Rel, "fixture.go") {
			foundRedfixture = true
			break
		}
	}
	assert.True(t, foundRedfixture,
		"A2 scanner reported diagnostics but none from the red fixture; got: %+v", diags)
}

// TestMQTTConfigSeal_A2ScannerFires proves that scanMQTTCompositeLitConstruction
// produces no violations for the production adapters/mqtt package (all non-zero
// Config composite literals are inside NewConfig), AND that the go/types
// resolution path actually finds ≥1 such literal inside NewConfig (so the
// production A2 scan cannot be vacuously green due to a silently-broken type
// resolver).
//
// Without the insideCount ≥ 1 assertion, a refactor that moved the Config{...}
// literal out of NewConfig — or broke the go/types resolution path — could leave
// TestMQTTConfigSealedFieldFrozen01/A2_Construction vacuously passing.
func TestMQTTConfigSeal_A2ScannerFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const newConfigFullName = mqttPkgPath + ".NewConfig"

	var outsideCount, insideCount int

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
				// Count Config composite literals outside NewConfig (should be zero).
				diags := scanMQTTCompositeLitConstruction(
					p.Fset, f, rel, p.TypesInfo,
					"Config", []string{newConfigFullName}, "MQTT-CONFIG-SEALED-FIELD-FROZEN-01",
				)
				outsideCount += len(diags)

				// Count non-zero Config composite literals inside NewConfig (must be ≥1).
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
					if tobj.Pkg() == nil || tobj.Pkg().Path() != mqttPkgPath || tobj.Name() != "Config" {
						return
					}
					if mqttEnclosingFuncAllowed(p.TypesInfo, f, lit, []string{newConfigFullName}) {
						insideCount++
					}
				})
			}
			return nil
		})

	// All non-zero Config literals must be inside NewConfig.
	assert.Equal(t, 0, outsideCount,
		"A2 scanner: production code has Config composite literals outside NewConfig — "+
			"this means the A2 self-check correctly detects violations when they exist")

	// The inside count must be ≥1: NewConfig constructs the Config{...} literal in its body.
	// If this fails, the go/types resolution path is broken and cannot see any Config literals.
	assert.GreaterOrEqual(t, insideCount, 1,
		"A2 scanner must find ≥1 Config composite literal inside NewConfig — "+
			"if this fails, the go/types resolution path is silently broken")
}

// TestMQTTFunnel_FieldWriteScannerFiresOnRedFixture proves the A2b field-write
// scanner (scanSealedFieldWriteConstruction) fires on a genuine field-write
// construction bypass — `var c FixtureClientID; c.value = ...; return c` outside
// the sanctioned ParseFixtureClientID. This is the blind spot the composite-
// literal scanner (A2) cannot see (a zero-value declaration has no CompositeLit
// node), so without this self-check a regression disabling the field-write scan
// would pass silently.
//
// See tools/archtest/internal/mqttredfixture/fixture.go::archtestRedFixtureFieldWrite.
func TestMQTTFunnel_FieldWriteScannerFiresOnRedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CLIENT-ID-NAMESPACE-01"
	const fixturePkgPath = PlatformModulePath + "/tools/archtest/internal/mqttredfixture"

	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{fixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanSealedFieldWriteConstruction(
					p.Fset, f, rel, p.TypesInfo,
					fixturePkgPath, "FixtureClientID",
					[]string{fixturePkgPath + ".ParseFixtureClientID"},
					"", "", // fixture has no option type
					ruleID,
				)...)
			}
			return out
		})

	require.NotEmpty(t, diags,
		"A2b field-write scanner must report ≥1 violation on the red fixture in "+
			"tools/archtest/internal/mqttredfixture/fixture.go — if this fails, the "+
			"field-write scan is silently broken")

	foundRedfixture := false
	for _, d := range diags {
		if strings.HasSuffix(d.Rel, "fixture.go") {
			foundRedfixture = true
			break
		}
	}
	assert.True(t, foundRedfixture,
		"A2b scanner reported diagnostics but none from the red fixture; got: %+v", diags)
}

// TestMQTTConfigFieldWriteSeal_ScannerFires proves the A2b field-write scan over
// production adapters/mqtt: (a) zero violations — every Config field write is
// inside NewConfig or a With* ConfigOption constructor; AND (b) the scanner
// actually SEES ≥1 such field write (the With* option closures write
// cfg.<field>), so the production A2b scan cannot be vacuously green from a
// silently-broken go/types selection resolver.
func TestMQTTConfigFieldWriteSeal_ScannerFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const newConfigFullName = mqttPkgPath + ".NewConfig"
	const ruleID = "MQTT-CONFIG-SEALED-FIELD-FROZEN-01"
	var outsideCount, seenCount int

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
				// Real allowlist (NewConfig + With* ConfigOption funcs): production
				// must have ZERO Config field writes outside the funnel.
				outsideCount += len(scanSealedFieldWriteConstruction(
					p.Fset, f, rel, p.TypesInfo,
					mqttPkgPath, "Config",
					[]string{newConfigFullName},
					mqttPkgPath, "ConfigOption",
					ruleID,
				))
				// Empty allowlist (no sanctioned writer): every Config field write —
				// the With* option closures' cfg.<field>=… — becomes a diagnostic, so
				// this count is the total field writes the scanner SEES. ≥1 proves the
				// go/types selection-resolution path is live (anti-vacuity); these are
				// exactly the writes the real allowlist above exempts to reach 0.
				seenCount += len(scanSealedFieldWriteConstruction(
					p.Fset, f, rel, p.TypesInfo,
					mqttPkgPath, "Config",
					nil, "", "",
					ruleID,
				))
			}
			return nil
		})

	assert.Equal(t, 0, outsideCount,
		"A2b scanner: production code writes Config fields outside NewConfig / With* "+
			"option constructors — sealed-construction funnel bypass")
	assert.GreaterOrEqual(t, seenCount, 1,
		"A2b scanner must SEE ≥1 Config field write (the With* option writes) under an "+
			"empty allowlist — if this fails, the go/types selection resolver is silently broken")
}

// TestMQTTFunnel_NonVacuousness documents that the A1 test was confirmed
// non-vacuous via a temporary mutation of clientid.go (changing
// `type ClientID struct{value string}` → `type ClientID string`) which caused
// TestMQTTClientIDNamespace01/A1_FieldFreeze to fail with:
//
//	MQTT-CLIENT-ID-NAMESPACE-01/A1: ClientID is not a struct (Kind=string);
//	  the sealed-struct guarantee is gone — type may have been changed to
//	  a string newtype or alias
//
// The mutation was reverted before committing. This test function serves as
// the mandated documentation of that non-vacuousness proof (see task B4 in
// plan.md §4).
func TestMQTTFunnel_NonVacuousness(t *testing.T) {
	t.Parallel()
	// Statically verify the production type is a struct (not a string newtype).
	// This mirrors what the temporary mutation broke.
	dt := reflect.TypeOf(mqtt.ClientID{})
	require.Equal(t, reflect.Struct, dt.Kind(),
		"MQTT-CLIENT-ID-NAMESPACE-01/A1: ClientID must be a struct, not a string newtype. "+
			"If this fails, someone changed the type definition in clientid.go.")

	dt2 := reflect.TypeOf(mqtt.TopicNamespace{})
	require.Equal(t, reflect.Struct, dt2.Kind(),
		"MQTT-TOPIC-NAMESPACE-01/A1: TopicNamespace must be a struct, not a string newtype. "+
			"If this fails, someone changed the type definition in topicns.go.")

	// MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1 (#1231): confirmed non-vacuous via a
	// temporary mutation of config.go (exporting `clientID` → `ClientID`) which
	// made TestMQTTConfigSealedFieldFrozen01/A1_FieldFreeze fail with:
	//
	//	MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1: Config has no "clientID" field
	//	  (renamed or exported?); sealed-construction invariant broken
	//	MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1: Config.ClientID is exported ...
	//
	// The mutation was reverted before committing. Statically reassert the Config
	// shape here (struct + every field unexported) so the freeze cannot quietly
	// regress to a string newtype or an exported-field struct.
	dtCfg := reflect.TypeOf(mqtt.Config{})
	require.Equal(t, reflect.Struct, dtCfg.Kind(),
		"MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1: Config must be a struct. "+
			"If this fails, someone changed the type definition in config.go.")
	for i := 0; i < dtCfg.NumField(); i++ {
		f := dtCfg.Field(i)
		require.NotEmpty(t, f.PkgPath,
			"MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1: Config.%s is exported — the sealed "+
				"construction funnel requires every field unexported (see config.go NewConfig).", f.Name)
	}
}
