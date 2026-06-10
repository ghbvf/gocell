// INVARIANT: MQTT-CLIENT-ID-NAMESPACE-01
// INVARIANT: MQTT-TOPIC-NAMESPACE-01
// INVARIANT: MQTT-CONFIG-SEALED-FIELD-FROZEN-01
//
// mqtt_funnel.go — importable sealed-struct construction funnel logic for
// adapters/mqtt.ClientID, adapters/mqtt.TopicNamespace, and adapters/mqtt.Config.
//
// This is the non-test home of the MQTT-CLIENT-ID-NAMESPACE-01 and
// MQTT-TOPIC-NAMESPACE-01 scanner helpers so they can be compiled by external
// Cell repositories through the CellRule pattern (Go never compiles a
// dependency's _test.go, so rule logic that external repos must run cannot live
// in a _test.go file). GoCell's own TestMQTTClientIDNamespace01 /
// TestMQTTTopicNamespace01 functions (mqtt_funnel_test.go) call the same shared
// helpers — single source, no parallel rule body.
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via the per-rule Tests).
//
// Platform-symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here.
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

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// mqttPkgPath is the canonical import path of the mqtt adapter package —
// derived from PlatformModulePath so no bare literal appears here.
const mqttPkgPath = PlatformModulePath + "/adapters/mqtt"

// mqttTopicNSPkgPath is the internal sub-package that owns the sealed token
// types (since #1247). Named differently from the topicnsPkgPath const in
// mqtt_callsite_funnel_test.go to avoid a duplicate-declaration error when the
// test binary links both the non-test .go and the _test.go into package archtest.
const mqttTopicNSPkgPath = mqttPkgPath + "/internal/topicns"

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

// mqttConfigField is one frozen field of the sealed adapters/mqtt.Config: its
// expected (unexported) name and reflect.Type identity. The full set is pinned
// by MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A1 (reflect schema freeze 范本) so that
// adding, removing, renaming, retyping, or EXPORTING any Config field trips the
// freeze and forces an ADR amendment + threat-model re-evaluation.
type mqttConfigField struct {
	name string
	typ  reflect.Type
}

// checkMQTTConfigFieldFreeze verifies dt is a struct whose field set EXACTLY
// matches want — same field count, same names, same type identities, and EVERY
// field unexported (PkgPath != ""). Unexported fields are the upstream Hard
// gate: an outside-package struct literal cannot set them, so the only way to
// obtain a non-zero Config is the sealed NewConfig constructor (which validates
// in its body). Exporting any field re-opens literal construction and is
// reported here. Returns violation messages (empty = clean).
//
// Multi-field sibling of checkMQTTSealedSingleValueField (one-field ClientID /
// TopicNamespace) and errcode_invariants.go's checkSealedKeyValueShape
// (two-field PublicDetail).
func checkMQTTConfigFieldFreeze(name string, dt reflect.Type, want []mqttConfigField) []string {
	var violations []string
	if dt.Kind() != reflect.Struct {
		return append(violations, fmt.Sprintf(
			"%s is not a struct (Kind=%s); the sealed-struct guarantee is gone — "+
				"type may have been changed to an alias or newtype", name, dt.Kind()))
	}
	if dt.NumField() != len(want) {
		violations = append(violations, fmt.Sprintf(
			"%s NumField = %d, want %d (adding/removing a field re-opens sealed "+
				"construction — update the ADR amendment + threat model first)",
			name, dt.NumField(), len(want)))
	}
	for _, w := range want {
		f, ok := dt.FieldByName(w.name)
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"%s has no %q field (renamed or exported?); sealed-construction invariant broken",
				name, w.name))
			continue
		}
		if f.PkgPath == "" {
			violations = append(violations, fmt.Sprintf(
				"%s.%s is exported (PkgPath empty); outside-package literal construction "+
					"becomes possible — lowercase it", name, w.name))
		}
		if f.Type != w.typ {
			violations = append(violations, fmt.Sprintf(
				"%s.%s type = %s, want %s (a field type change can alter wire/validation semantics)",
				name, w.name, f.Type, w.typ))
		}
	}
	return violations
}

// ─── A2: CompositeLit construction allowlist scanner ─────────────────────────

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

// isSealedCompositeLitViolation checks whether lit is a non-zero composite
// literal of (targetPkgPath, targetTypeName) outside the allowed funcs.
// Returns the Diagnostic to append, or a zero Diagnostic with empty Message.
func isSealedCompositeLitViolation(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	lit *ast.CompositeLit,
	targetPkgPath, targetTypeName string,
	allowedFullNames []string,
	ruleID string,
) (Diagnostic, bool) {
	if lit.Type == nil {
		return Diagnostic{}, false
	}
	tv, ok := info.Types[lit.Type]
	if !ok {
		return Diagnostic{}, false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return Diagnostic{}, false
	}
	tobj := named.Obj()
	if tobj.Pkg() == nil || tobj.Pkg().Path() != targetPkgPath || tobj.Name() != targetTypeName {
		return Diagnostic{}, false
	}
	if len(lit.Elts) == 0 {
		return Diagnostic{}, false // zero-value literal — allowed
	}
	if mqttEnclosingFuncAllowed(info, file, lit, allowedFullNames) {
		return Diagnostic{}, false
	}
	pos := fset.Position(lit.Pos())
	return Diagnostic{
		Rel:  rel,
		Line: pos.Line,
		Message: fmt.Sprintf(
			"%s/A2: %s composite literal at %s:%d is not enclosed in any of %v — "+
				"non-zero %s construction must go through an allowed constructor",
			ruleID, targetTypeName, rel, pos.Line, allowedFullNames, targetTypeName,
		),
	}, true
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
		if d, ok := isSealedCompositeLitViolation(
			fset, file, rel, info, lit,
			targetPkgPath, targetTypeName, allowedFullNames, ruleID,
		); ok {
			out = append(out, d)
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── A2b: field-write construction allowlist scanner ─────────────────────────

// isSealedFieldWrite reports whether sel selects a field of the sealed struct
// (targetPkgPath, targetTypeName) — i.e. `x.field` where x is of type T or *T.
// It uses info.Selections (FieldVal kind only), so a package-qualified
// identifier (pkg.Name, which is a Uses entry, not a field selection) is never
// mistaken for a field write.
func isSealedFieldWrite(info *types.Info, sel *ast.SelectorExpr, targetPkgPath, targetTypeName string) bool {
	selection, ok := info.Selections[sel]
	if !ok || selection.Kind() != types.FieldVal {
		return false
	}
	recv := selection.Recv()
	if ptr, isPtr := recv.(*types.Pointer); isPtr {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == targetPkgPath && obj.Name() == targetTypeName
}

// funcReturnsNamed reports whether fn's signature returns the named type
// (pkgPath, typeName) in any result position. Used to allow field writes inside
// option constructors (e.g. the mqtt.ConfigOption-returning With* funcs): the
// returned closure mutates the sealed struct only inside NewConfig's option loop,
// which validates afterwards.
func funcReturnsNamed(fn *types.Func, pkgPath, typeName string) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	results := sig.Results()
	for i := 0; i < results.Len(); i++ {
		named, isNamed := results.At(i).Type().(*types.Named)
		if !isNamed {
			continue
		}
		obj := named.Obj()
		if obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName {
			return true
		}
	}
	return false
}

// mqttFieldWriteAllowed reports whether a field write at node is sanctioned: it
// is enclosed in a func whose typed FullName() is in allowedFullNames, OR (when
// optionTypeName != "") a func whose signature returns the option type
// (optionPkgPath, optionTypeName). A write outside any FuncDecl body (package-var
// init, etc.) is NOT allowed (closed), matching mqttEnclosingFuncAllowed.
func mqttFieldWriteAllowed(
	info *types.Info, file *ast.File, node ast.Node,
	allowedFullNames []string, optionPkgPath, optionTypeName string,
) bool {
	fn, ok := ResolveEnclosingFunc(info, file, node)
	if !ok || fn == nil {
		return false
	}
	for _, key := range allowedFullNames {
		if fn.FullName() == key {
			return true
		}
	}
	return optionTypeName != "" && funcReturnsNamed(fn, optionPkgPath, optionTypeName)
}

// scanSealedFieldWriteConstruction is the A2b scanner: it reports a diagnostic
// for every assignment that writes a field of the sealed struct
// (targetPkgPath, targetTypeName) from a location NOT sanctioned by
// mqttFieldWriteAllowed. This closes the A2 composite-literal scanner's blind
// spot, where field-by-field construction —
//
//	var c Config        // zero-value declaration — allowed
//	c.clientID = id     // ← field-write construction of a non-zero Config
//	return c            // escapes unvalidated
//
// — would otherwise build an unvalidated sealed struct in-package without ever
// going through NewConfig.
//
// Sanctioned writers: the constructors in allowedFullNames (NewConfig — defensive,
// it actually builds via a composite literal, but allowing it keeps the rule
// robust to a future refactor; Parse / assembleClientID for the single-field
// structs) and every option constructor whose signature returns
// (optionPkgPath, optionTypeName) — the mqtt.ConfigOption With* funcs. The
// single-field ClientID / Namespace structs have no option type, so optionTypeName
// is "" and only their Parse/assemble constructor may write.
//
// # Residual blind spot
//
// A2b does NOT catch a ConfigOption applied to a locally-declared Config OUTSIDE
// NewConfig, e.g. `var c Config; WithTLS(x)(&c); use(c)` — the field write happens
// inside WithTLS (allowed) and `WithTLS(x)(&c)` is a call, not a field write.
// Production has no such pattern; closing it would need a separate
// ConfigOption-callsite funnel (out of scope for #1231). Documented here so it is
// not mistaken for covered.
func scanSealedFieldWriteConstruction(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	targetPkgPath string,
	targetTypeName string,
	allowedFullNames []string,
	optionPkgPath string,
	optionTypeName string,
	ruleID string,
) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic

	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if !isSealedFieldWrite(info, sel, targetPkgPath, targetTypeName) {
				continue
			}
			if mqttFieldWriteAllowed(info, file, sel, allowedFullNames, optionPkgPath, optionTypeName) {
				continue
			}
			pos := fset.Position(sel.Pos())
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"%s/A2b: %s field write at %s:%d constructs a non-zero %s outside %v "+
						"(and outside any sanctioned option constructor) — sealed-struct "+
						"construction must go through the validating constructor",
					ruleID, targetTypeName, rel, pos.Line, targetTypeName, allowedFullNames,
				),
			})
		}
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

// ─── Check* driver helpers ────────────────────────────────────────────────────

// collectClientIDDiags is the per-Pass body for CheckMQTTClientIDNamespace,
// extracted to keep the Check* func within gocognit ≤15.
func collectClientIDDiags(p *Pass, a2Diags, a3Diags *[]Diagnostic) {
	if p.Pkg == nil || p.TypesInfo == nil {
		return
	}
	const ruleID = "MQTT-CLIENT-ID-NAMESPACE-01"
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if p.Pkg.Path() == mqttPkgPath {
			*a2Diags = append(*a2Diags, scanMQTTCompositeLitConstruction(
				p.Fset, f, rel, p.TypesInfo,
				"ClientID",
				[]string{mqttPkgPath + ".assembleClientID"},
				ruleID,
			)...)
			*a2Diags = append(*a2Diags, scanSealedFieldWriteConstruction(
				p.Fset, f, rel, p.TypesInfo,
				mqttPkgPath, "ClientID",
				[]string{mqttPkgPath + ".assembleClientID"},
				"", "", // single-field sealed struct: no option type
				ruleID,
			)...)
		}
		*a3Diags = append(*a3Diags, scanMQTTTypeAliases(
			p.Fset, f, rel, p.TypesInfo,
			mqttPkgPath, "ClientID", "",
			p.Pkg.Path(), ruleID,
		)...)
	}
}

// collectTopicNSDiags is the per-Pass body for CheckMQTTTopicNamespace,
// extracted to keep the Check* func within gocognit ≤15.
func collectTopicNSDiags(p *Pass, a2Diags, a3Diags *[]Diagnostic) {
	if p.Pkg == nil || p.TypesInfo == nil {
		return
	}
	const ruleID = "MQTT-TOPIC-NAMESPACE-01"
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if p.Pkg.Path() == mqttTopicNSPkgPath {
			*a2Diags = append(*a2Diags, scanSealedCompositeLitConstruction(
				p.Fset, f, rel, p.TypesInfo,
				mqttTopicNSPkgPath, "Namespace",
				[]string{mqttTopicNSPkgPath + ".Parse"},
				ruleID,
			)...)
			*a2Diags = append(*a2Diags, scanSealedFieldWriteConstruction(
				p.Fset, f, rel, p.TypesInfo,
				mqttTopicNSPkgPath, "Namespace",
				[]string{mqttTopicNSPkgPath + ".Parse"},
				"", "", // single-field sealed struct: no option type
				ruleID,
			)...)
		}
		*a3Diags = append(*a3Diags, scanMQTTTypeAliases(
			p.Fset, f, rel, p.TypesInfo,
			mqttTopicNSPkgPath, "Namespace", "TopicNamespace",
			p.Pkg.Path(), ruleID,
		)...)
	}
}

// CheckMQTTClientIDNamespace runs the MQTT-CLIENT-ID-NAMESPACE-01 A2 (composite
// literal) + A2b (field write) + A3 (alias) scans (the A1 field-freeze is
// reflect-based and lives in the dogfood Test).
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via the per-rule Tests).
func CheckMQTTClientIDNamespace(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	var a2Diags, a3Diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: cfg.BuildTags},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			collectClientIDDiags(p, &a2Diags, &a3Diags)
			return nil
		})
	return append(a2Diags, a3Diags...)
}

// CheckMQTTTopicNamespace runs the MQTT-TOPIC-NAMESPACE-01 A2 (composite literal)
// + A2b (field write) + A3 (alias) scans (the A1 field-freeze is reflect-based
// and lives in the dogfood Test).
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via the per-rule Tests).
func CheckMQTTTopicNamespace(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	var a2Diags, a3Diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: cfg.BuildTags},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			collectTopicNSDiags(p, &a2Diags, &a3Diags)
			return nil
		})
	return append(a2Diags, a3Diags...)
}

// collectConfigDiags is the per-Pass body for CheckMQTTConfigSeal: it scans
// adapters/mqtt production files for in-package Config construction outside the
// sanctioned funnel (the downstream half of the sealed-construction funnel; the
// upstream Hard half — unexported fields blocking outside-package literals — and
// the A1 field-freeze are reflect-based and live in the dogfood Test). Two
// complementary scans:
//
//   - A2 (composite literal): non-zero `Config{...}` literals must be inside
//     NewConfig. Zero-value `Config{}` (NewConfig's error-return path) is allowed
//     by the shared scanner's len(Elts)==0 skip.
//   - A2b (field write): `c.field = x` writes to a Config field must be inside
//     NewConfig or a With* option constructor (signature returns ConfigOption) —
//     closing the field-by-field construction blind spot the composite-literal
//     scan alone misses (`var c Config; c.clientID = id; return c`).
func collectConfigDiags(p *Pass, a2Diags *[]Diagnostic) {
	if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
		return
	}
	const ruleID = "MQTT-CONFIG-SEALED-FIELD-FROZEN-01"
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		*a2Diags = append(*a2Diags, scanMQTTCompositeLitConstruction(
			p.Fset, f, rel, p.TypesInfo,
			"Config",
			[]string{mqttPkgPath + ".NewConfig"},
			ruleID,
		)...)
		*a2Diags = append(*a2Diags, scanSealedFieldWriteConstruction(
			p.Fset, f, rel, p.TypesInfo,
			mqttPkgPath, "Config",
			[]string{mqttPkgPath + ".NewConfig"},
			mqttPkgPath, "ConfigOption",
			ruleID,
		)...)
	}
}

// CheckMQTTConfigSeal runs the MQTT-CONFIG-SEALED-FIELD-FROZEN-01 A2 (composite
// literal) + A2b (field write) construction scans (the A1 reflect field-freeze
// lives in the dogfood Test). Together they lock in-package non-zero Config
// construction to NewConfig + the With* option constructors; outside-package
// construction is already impossible at compile time via the unexported fields
// the A1 freeze pins.
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization + fork-safety, dogfooded via the per-rule Tests).
func CheckMQTTConfigSeal(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	var a2Diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: cfg.BuildTags},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			collectConfigDiags(p, &a2Diags)
			return nil
		})
	return a2Diags
}
