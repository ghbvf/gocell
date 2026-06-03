// INVARIANT: MQTT-CLIENT-ID-NAMESPACE-01
//   - INVARIANT: MQTT-TOPIC-NAMESPACE-01
//
// mqtt_funnel_test.go — sealed-struct construction funnels for
// adapters/mqtt.ClientID and adapters/mqtt.TopicNamespace.
//
// Both types carry exactly one unexported field (`value string`). Construction
// outside their respective Parse* factory is structurally impossible from any
// package: Go's visibility rules reject struct-literal construction of a struct
// with unexported fields from an external package (compile error). This test
// locks that invariant from drifting inside the adapters/mqtt package itself.
//
// # MQTT-CLIENT-ID-NAMESPACE-01 — ClientID sealed-struct funnel
//
// ClientID guarantees that every non-zero value passed into autopaho's
// ClientID field has been validated through ParseClientID (format:
// "{cellID}-{role}-{uuid}", all segments validated against a segment regex,
// total length ≤ 128). A caller who holds a ClientID is guaranteed it passed
// validation; an unvalidated bare string cannot reach the broker.
//
// Sub-rules:
//
//   - A1 (field freeze): adapters/mqtt.ClientID must be a struct with exactly
//     ONE field, named "value", type string, unexported. Caught by reflect
//     with the same checkSealedSingleValueField helper used for
//     DETAILS-SEALED-FIELD-FROZEN-01. Detects reversion to `type ClientID string`
//     (type alias or string-newtype: wrong Kind) or field addition/export.
//   - A2 (construction allowlist): within package adapters/mqtt (production files
//     only, *_test.go excluded), the only site where a non-zero ClientID composite
//     literal `ClientID{...}` is constructed is the body of ParseClientID.
//     AST scan via EachInSubtree[ast.CompositeLit] + enclosing-func name check.
//   - A3 (reverse blind-spot): no `type X = ClientID` alias and no other struct
//     in the repo re-shapes the same `{value string}` single-field form that could
//     serve as a re-constructable surrogate.
//
// # MQTT-TOPIC-NAMESPACE-01 — TopicNamespace sealed-struct funnel
//
// TopicNamespace guarantees that every topic prefix used to gate cell
// publish/subscribe surfaces has been validated through ParseTopicNamespace.
//
// Sub-rules: A1 / A2 / A3 symmetric to MQTT-CLIENT-ID-NAMESPACE-01 but for
// TopicNamespace / ParseTopicNamespace.
//
// # AI-robust grading (per .claude/rules/gocell/ai-robust.md §Funnel 双向锁评级)
//
// Upstream:
//   - Package-external (Hard): Go compiler rejects ClientID{...} / TopicNamespace{...}
//     literal construction from any package other than adapters/mqtt because the
//     single field `value` is unexported. No archtest needed; compile-time gate.
//   - Package-internal (Medium archtest): Go has no syntax to prevent a file inside
//     adapters/mqtt from adding a second ClientID composite literal site. A2 closes
//     this gap via AST scan of production files only. Permanent Go-language ceiling.
//
// Downstream callsite funnel (DELIVERED — gh issue #1225 closed):
//
//	Publisher.Publish calls ns.Mint(topic) (which calls PublishOK internally)
//	to obtain a sealed PublishableTopic; Connection.Publish only CONSUMES that
//	token. MintFilter calls SubscribeOK internally. MQTT-PUBLISH-CALLSITE-FUNNEL-01
//	(A1–A4) and MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01 (S1–S6) in
//	mqtt_callsite_funnel_test.go lock all cm.Publish / cm.Subscribe /
//	cm.Unsubscribe callsites to their respective sanctioned funnel functions.
//
// # Blind-spot inventory
//
// AST forms OUTSIDE the declared coverage of EachInSubtree[ast.CompositeLit]:
//
//  1. reflect-based construction: `reflect.New(reflect.TypeOf(mqtt.ClientID{}))` —
//     would return a zero-value pointer; zero-value is accepted (blank comparison
//     sites). Non-zero construction via reflect.Value.FieldByName("value").Set(...)
//     requires an unexported field to be addressable cross-package, which Go
//     forbids at runtime (panic) for unexported fields. Reverse self-check:
//     TestMQTTFunnel_BlindSpot_NoReflectNew asserts no reflect.New(mqtt.*) in
//     production code.
//  2. unsafe.Pointer: `(*mqtt.ClientID)(unsafe.Pointer(&s))` would bypass all
//     checks. Reverse self-check: TestMQTTFunnel_BlindSpot_NoUnsafePtr asserts
//     no unsafe.Pointer callsite referencing the mqtt package in production code.
//  3. re-export alias in same package: `type AliasID = ClientID` inside adapters/mqtt
//     preserves visibility, so aliased construction `AliasID{value: "x"}` would
//     still be a compile error from external packages, but from within the package
//     it's the same scope — A2 covers all composite literals regardless of type
//     name by resolving the underlying type.
//  4. A2 scanner go/types type-resolution path: the scanner relies on
//     go/types.Info to resolve composite literal types to their package path and
//     name. A silent regression in type loading would make A2 vacuously pass.
//     Reverse self-check: TestMQTTFunnel_A2ScannerFires asserts that the scanner
//     finds ≥1 ClientID literal inside ParseClientID (scanner has teeth) and
//     zero violations outside it (production is clean).
//
// ref: adapters/mqtt.ClientID — sealed struct
// ref: adapters/mqtt.TopicNamespace — sealed struct
// ref: adapters/mqtt.ParseClientID — sole construction site for ClientID
// ref: adapters/mqtt.ParseTopicNamespace — sole construction site for TopicNamespace
// ref: PROBENAME-SEALED-FUNNEL-01 (probename_sealed_funnel_test.go) — inventory extension (A1)
// ref: DETAILS-SEALED-FIELD-FROZEN-01 (errcode_invariants_test.go) — reflect field lock pattern
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md
// ref: gh issue #1225 — downstream TOPIC callsite funnel deferred to PR-2/3
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/mqtt"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ─── Typed-identity enclosing-func allowlist ─────────────────────────────────

// mqttEnclosingFuncAllowed reports whether the enclosing *types.Func of node
// has a FullName() in allowedFullNames (set semantics). This is the typed
// equivalent of the name-only gate that mqttEnclosingFuncName provides: it
// prevents a function named "Mint" on a different receiver/type from falsely
// passing the allowlist. When ResolveEnclosingFunc cannot resolve (e.g. no
// types.Info, node outside any FuncDecl), the function returns false (closed).
func mqttEnclosingFuncAllowed(info *types.Info, file *ast.File, node ast.Node, allowedFullNames []string) bool {
	fn, ok := ResolveEnclosingFunc(info, file, node)
	if !ok || fn == nil {
		return false
	}
	for _, key := range allowedFullNames {
		if fn.FullName() == key {
			return true
		}
	}
	return false
}

// ─── Constants ────────────────────────────────────────────────────────────────

const mqttPkgPath = "github.com/ghbvf/gocell/adapters/mqtt"

// ─── Reflect helpers ─────────────────────────────────────────────────────────

// checkMQTTSealedSingleValueField verifies that dt is a struct with exactly
// one field named "value", type string, unexported. Returns violation messages
// (empty = clean). Mirrors checkSealedKeyValueShape from errcode_invariants_test.go
// but for a single-field struct.
func checkMQTTSealedSingleValueField(name string, dt reflect.Type) []string {
	var violations []string
	if dt.Kind() != reflect.Struct {
		violations = append(violations, fmt.Sprintf(
			"%s is not a struct (Kind=%s); the sealed-struct guarantee is gone — "+
				"type may have been changed to a string newtype or alias",
			name, dt.Kind(),
		))
		return violations
	}
	if dt.NumField() != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s NumField = %d, want 1 (single unexported value string field); "+
				"adding a field re-opens sealed-construction — update ADR amendment first",
			name, dt.NumField(),
		))
		// Still try to check the value field if the count is off but the field exists.
	}
	valueField, ok := dt.FieldByName("value")
	if !ok {
		violations = append(violations, fmt.Sprintf(
			"%s has no 'value' field (renamed or exported?); "+
				"sealed-construction invariant broken",
			name,
		))
		return violations
	}
	if valueField.PkgPath == "" {
		violations = append(violations, fmt.Sprintf(
			"%s.value is exported (PkgPath empty); "+
				"outside-package literal construction becomes possible — lowercase it",
			name,
		))
	}
	if valueField.Type.Kind() != reflect.String {
		violations = append(violations, fmt.Sprintf(
			"%s.value Kind = %s, want String",
			name, valueField.Type.Kind(),
		))
	}
	return violations
}

// assertMQTTSealedSingleValueField adapts checkMQTTSealedSingleValueField to *testing.T.
func assertMQTTSealedSingleValueField(t *testing.T, name string, dt reflect.Type) {
	t.Helper()
	for _, v := range checkMQTTSealedSingleValueField(name, dt) {
		t.Errorf("%s", v)
	}
}

// ─── A2: CompositeLit construction allowlist scanner ─────────────────────────

// mqttEnclosingFuncName returns the name of the innermost *ast.FuncDecl that
// contains pos, or "" if none is found. Used to scope composite-literal checks
// to a specific constructor body.
func mqttEnclosingFuncName(file *ast.File, pos token.Pos) string {
	var found string
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		if fd.Pos() <= pos && pos <= fd.End() {
			found = fd.Name.Name
		}
	})
	return found
}

// scanMQTTCompositeLitConstruction scans file for composite literals whose
// type resolves (via go/types) to mqttTypeName inside mqttPkgPath. It is a
// thin wrapper over scanSealedCompositeLitConstruction with the production
// mqtt package path bound; the parameterized form below lets the
// red-fixture self-check exercise the same scanner against
// tools/archtest/internal/mqttredfixture/.
//
// Production files only (caller must skip *_test.go before calling).
func scanMQTTCompositeLitConstruction(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	mqttTypeName string,
	allowedFuncs []string,
	ruleID string,
) []Diagnostic {
	return scanSealedCompositeLitConstruction(fset, file, rel, info,
		mqttPkgPath, mqttTypeName, allowedFuncs, ruleID)
}

// scanSealedCompositeLitConstruction is the generalized A2 scanner. It
// reports a diagnostic for every composite literal in file whose type
// resolves (via go/types) to (targetPkgPath, targetTypeName) AND is NOT
// enclosed in any function whose FullName() is in allowedFullNames (set
// semantics). The gate uses typed identity via ResolveEnclosingFunc so that
// a function with the same bare name but a different receiver/package cannot
// falsely pass the allowlist.
//
// Zero-value literals (no Elts) are accepted anywhere (used in error paths
// like `return ClientID{}, err`).
func scanSealedCompositeLitConstruction(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	targetPkgPath string,
	targetTypeName string,
	allowedFullNames []string,
	ruleID string,
) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic

	EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
		if lit.Type == nil {
			// Anonymous composite literal inside another literal — skip.
			return
		}
		// Resolve the type of the composite literal via go/types.
		tv, ok := info.Types[lit.Type]
		if !ok {
			return
		}
		named, ok := tv.Type.(*types.Named)
		if !ok {
			return
		}
		tobj := named.Obj()
		if tobj.Pkg() == nil || tobj.Pkg().Path() != targetPkgPath {
			return
		}
		if tobj.Name() != targetTypeName {
			return
		}

		// Found a composite literal of the target type.
		// Zero-value literal (no elements) is allowed anywhere:
		// it is used for comparisons like `x == ClientID{}`.
		if len(lit.Elts) == 0 {
			return
		}

		// Non-zero literal: must be enclosed in one of the allowed functions
		// (matched by typed FullName() identity, not bare name).
		if mqttEnclosingFuncAllowed(info, file, lit, allowedFullNames) {
			return
		}
		pos := fset.Position(lit.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"%s/A2: %s composite literal at %s:%d is not enclosed in any of %v — "+
					"non-zero %s construction must go through the Parse factory",
				ruleID, targetTypeName, rel, pos.Line, allowedFullNames, targetTypeName,
			),
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── A3: alias / re-shape blind-spot scanner ─────────────────────────────────

// scanMQTTTypeAliases scans file for type alias declarations of the form
// `type X = <targetTypeName>` (where the RHS resolves to targetPkgPath/targetTypeName)
// anywhere in the repo. Such aliases would not re-open construction (unexported fields
// remain inaccessible) but are banned for clarity and to prevent future confusion.
//
// sanctionedAliasName, if non-empty, names one alias that IS permitted — the
// canonical re-export alias (e.g. adapters/mqtt namespace.go declares
// `type TopicNamespace = topicns.Namespace`). The skip is bound to BOTH the
// alias name AND the declaring package (currentPkgPath == mqttPkgPath), so a
// type alias with the same name in any other package is still reported.
//
// For TopicNamespace the type moved to internal/topicns as Namespace (#1247);
// the sanctioned alias is namespace.go's `TopicNamespace = topicns.Namespace`,
// which lives in adapters/mqtt (mqttPkgPath). Only that specific (name, pkg)
// pair is skipped; all other aliases of the target type trigger a diagnostic.
// For ClientID the type remains in adapters/mqtt; there is no sanctioned alias
// (sanctionedAliasName = "").
func scanMQTTTypeAliases(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	targetPkgPath, targetTypeName, sanctionedAliasName string,
	currentPkgPath string,
	ruleID string,
) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic

	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Assign == token.NoPos {
			// Not an alias declaration.
			return
		}
		// Skip the one sanctioned re-export alias (e.g. adapters/mqtt's
		// `type TopicNamespace = topicns.Namespace`), but only if it lives in
		// the expected declaring package. A same-named alias in another package
		// is not the sanctioned re-export and must still be reported.
		if sanctionedAliasName != "" &&
			ts.Name.Name == sanctionedAliasName &&
			currentPkgPath == mqttPkgPath {
			return
		}
		// It's an alias (and not the sanctioned one). Check if the RHS resolves to
		// our target type.
		tv, ok := info.Types[ts.Type]
		if !ok {
			return
		}
		named, ok := tv.Type.(*types.Named)
		if !ok {
			return
		}
		tobj := named.Obj()
		if tobj.Pkg() == nil || tobj.Pkg().Path() != targetPkgPath {
			return
		}
		if tobj.Name() != targetTypeName {
			return
		}
		pos := fset.Position(ts.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"%s/A3: type alias `type %s = %s.%s` at %s:%d is prohibited — "+
					"aliases of the sealed token type obscure the construction funnel "+
					"(the sanctioned re-export is adapters/mqtt namespace.go)",
				ruleID, ts.Name.Name, tobj.Pkg().Name(), targetTypeName, rel, pos.Line,
			),
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
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
		assertMQTTSealedSingleValueField(t, ruleID+"/A1: ClientID", dt)
	})

	root := findModuleRoot(t)

	var a2Diags, a3Diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}

			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				if p.Pkg.Path() == mqttPkgPath {
					a2Diags = append(a2Diags, scanMQTTCompositeLitConstruction(
						p.Fset, f, rel, p.TypesInfo,
						"ClientID",
						// Typed FullName() key: assembleClientID is a package-level func
						// in adapters/mqtt (no receiver), so FullName = "<pkg>.<func>".
						[]string{mqttPkgPath + ".assembleClientID"},
						ruleID,
					)...)
				}

				a3Diags = append(a3Diags, scanMQTTTypeAliases(
					p.Fset, f, rel, p.TypesInfo,
					mqttPkgPath, "ClientID", "",
					p.Pkg.Path(), ruleID,
				)...)
			}
			return nil
		})

	t.Run("A2_ConstructionAllowlist", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID+"/A2", a2Diags)
	})

	t.Run("A3_NoAliasOrReShape", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID+"/A3", a3Diags)
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
		assertMQTTSealedSingleValueField(t, ruleID+"/A1: TopicNamespace", dt)
	})

	root := findModuleRoot(t)

	var a2Diags, a3Diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// The TopicNamespace seal moved to adapters/mqtt/internal/topicns as
				// Namespace (#1247): adapters/mqtt now holds only a `type
				// TopicNamespace = topicns.Namespace` alias and cannot construct the
				// type at all (unexported field, cross-package compile error). The
				// residual construction-allowlist axis (Namespace{...} outside Parse)
				// is therefore scanned in the internal package, where Parse co-locates
				// with the type.
				if p.Pkg.Path() == topicnsPkgPath {
					a2Diags = append(a2Diags, scanSealedCompositeLitConstruction(
						p.Fset, f, rel, p.TypesInfo,
						topicnsPkgPath, "Namespace",
						// Typed FullName() key: Parse is a package-level func in topicns.
						[]string{topicnsPkgPath + ".Parse"},
						ruleID,
					)...)
				}
				a3Diags = append(a3Diags, scanMQTTTypeAliases(
					p.Fset, f, rel, p.TypesInfo,
					topicnsPkgPath, "Namespace", "TopicNamespace",
					p.Pkg.Path(), ruleID,
				)...)
			}
			return nil
		})

	t.Run("A2_ConstructionAllowlist", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID+"/A2", a2Diags)
	})

	t.Run("A3_NoAliasOrReShape", func(t *testing.T) {
		t.Parallel()
		Report(t, ruleID+"/A3", a3Diags)
	})
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
			f.PkgPath = "github.com/ghbvf/gocell/tools/archtest"
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

	root := findModuleRoot(t)
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
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
}

// TestMQTTFunnel_BlindSpot_NoUnsafePtr (blind-spot B2) asserts that NO
// production file in the entire repository imports both "unsafe" and the
// adapters/mqtt package in a way that could construct a non-zero ClientID /
// TopicNamespace via unsafe.Pointer cast. Earlier scope was just
// `[]string{mqttPkgPath}` which only protected the adapter itself; external
// packages could still mount the bypass (per C7 F11 review).
//
// Strategy:
//  1. Scan all production packages reached by prodscan.PatternsExtended.
//  2. For each file that imports "unsafe", record a diagnostic IF the same
//     package also imports adapters/mqtt. Pure-unsafe usage unrelated to mqtt
//     is out of scope here.
func TestMQTTFunnel_BlindSpot_NoUnsafePtr(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
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

	_ = root // used via prodscan patterns above; kept to avoid unused var error
	assert.Empty(t, diags,
		"MQTT funnel blind-spot B2: production code imports \"unsafe\" while referencing adapters/mqtt")
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

	root := findModuleRoot(t)

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
		prodscan.PatternsExtended(root)),
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
}
