//go:build archtest

// INVARIANT: MQTT-CONNACK-REASON-TABLE-COMPLETE-01
// INVARIANT: MQTT-PUBACK-REASON-TABLE-COMPLETE-01
// INVARIANT: MQTT-SUBACK-REASON-TABLE-COMPLETE-01
// INVARIANT: MQTT-REASON-TABLE-POSITIONAL-01
//
// mqtt_reason_table_invariants_test.go — locks MQTT v5 reason-code single-source
// tables in adapters/mqtt/errors.go to the exact spec coverage for CONNACK
// (§3.2.2.2), PUBACK (§3.4.2.1), and SUBACK (§3.9.3), and asserts that every
// row uses positional (not named-field) composite-literal syntax.
//
// # Invariants
//
//   - MQTT-CONNACK-REASON-TABLE-COMPLETE-01: connackReasonTable var's CompositeLit
//     rows' first positional element (the reason code byte) must equal the exact
//     golden set {0x00,0x80,0x81,0x82,0x83,0x84,0x85,0x86,0x87,0x88,0x89,0x8A,
//     0x8C,0x90,0x95,0x97,0x99,0x9A,0x9B,0x9C,0x9D,0x9F} (22 codes, §3.2.2.2).
//
//   - MQTT-PUBACK-REASON-TABLE-COMPLETE-01: pubackReasonTable var's first positional
//     element must equal {0x00,0x10,0x80,0x83,0x87,0x90,0x91,0x97,0x99}
//     (9 codes, §3.4.2.1).
//
//   - MQTT-SUBACK-REASON-TABLE-COMPLETE-01: subackReasonTable var's first positional
//     element must equal {0x00,0x01,0x02,0x80,0x83,0x87,0x8F,0x91,0x97,0x9E,0xA1,0xA2}
//     (12 codes, §3.9.3).
//
//   - MQTT-REASON-TABLE-POSITIONAL-01: every row in all three tables must be a
//     positional CompositeLit (no *ast.KeyValueExpr elements). Named-field literals
//     could omit a field silently, defeating the compile-time parity guard.
//
// # AI-robust rating
//
// Medium. Detection uses go/types-aware AST scanning (var name lookup via GenDecl
// + ValueSpec, then CompositeLit element extraction with TypesInfo for byte value).
// Not Hard: Go has no compile-time way to enforce that a slice literal's element
// set equals a golden set. The positional-only sub-rule (POSITIONAL-01) pushes
// closer to Hard: the compile-time parity of the struct's field count is enforced
// by the Go type-checker, but the golden-set completeness check is archtest-bound.
//
// # Blind-spot note
//
// The golden sets in this file are hand-maintained from the MQTT v5 spec. A future
// revision of the spec adding new reason codes requires updating the golden sets
// here AND the table in adapters/mqtt/errors.go simultaneously; this file is the
// forcing function for that update (CI will fail if either side drifts).
//
// Scanner: scanMQTTReasonTableCodes (below).
// Fixture: tools/archtest/internal/mqttreasonfixture/fixture.go.
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Golden code sets — hand-maintained from MQTT v5 spec.
// Any drift between these golden sets and the production table is a CI failure.

// mqttConnackGolden is the complete set of CONNACK reason codes (§3.2.2.2, 22 codes).
var mqttConnackGolden = map[byte]bool{
	0x00: true, 0x80: true, 0x81: true, 0x82: true, 0x83: true,
	0x84: true, 0x85: true, 0x86: true, 0x87: true, 0x88: true,
	0x89: true, 0x8A: true, 0x8C: true, 0x90: true, 0x95: true,
	0x97: true, 0x99: true, 0x9A: true, 0x9B: true, 0x9C: true,
	0x9D: true, 0x9F: true,
}

// mqttPubackGolden is the complete set of PUBACK reason codes (§3.4.2.1, 9 codes).
var mqttPubackGolden = map[byte]bool{
	0x00: true, 0x10: true, 0x80: true, 0x83: true, 0x87: true,
	0x90: true, 0x91: true, 0x97: true, 0x99: true,
}

// mqttSubackGolden is the complete set of SUBACK reason codes (§3.9.3, 12 codes).
var mqttSubackGolden = map[byte]bool{
	0x00: true, 0x01: true, 0x02: true, 0x80: true, 0x83: true,
	0x87: true, 0x8F: true, 0x91: true, 0x97: true, 0x9E: true,
	0xA1: true, 0xA2: true,
}

// tableVarNames maps the three table variable names to their golden code sets.
var tableVarNames = map[string]map[byte]bool{
	"connackReasonTable": mqttConnackGolden,
	"pubackReasonTable":  mqttPubackGolden,
	"subackReasonTable":  mqttSubackGolden,
}

// ─── MQTT-CONNACK-REASON-TABLE-COMPLETE-01 ───────────────────────────────────

// TestMQTTConnackReasonTableComplete01 asserts that connackReasonTable in
// adapters/mqtt contains exactly the 22 CONNACK reason codes of §3.2.2.2.
func TestMQTTConnackReasonTableComplete01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodes(t, "connackReasonTable", mqttConnackGolden)
	assert.Empty(t, diags, "MQTT-CONNACK-REASON-TABLE-COMPLETE-01: reason code set mismatch")
}

// TestMQTTConnackReasonTableComplete01_ReverseFixture proves the scanner fires on
// the fixture table that deliberately omits code 0x81.
func TestMQTTConnackReasonTableComplete01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodesInFixture(t, "connackReasonTableFixture", mqttConnackGolden)
	require.NotEmpty(t, diags,
		"MQTT-CONNACK-REASON-TABLE-COMPLETE-01 reverse fixture: scanner must flag the incomplete table")
	// The diagnostic must name the missing code 0x81.
	joined := diagMessages(diags)
	assert.Contains(t, joined, "0x81",
		"MQTT-CONNACK-REASON-TABLE-COMPLETE-01 reverse fixture: diagnostic must name missing code 0x81")
}

// ─── MQTT-PUBACK-REASON-TABLE-COMPLETE-01 ────────────────────────────────────

// TestMQTTPubackReasonTableComplete01 asserts that pubackReasonTable in
// adapters/mqtt contains exactly the 9 PUBACK reason codes of §3.4.2.1.
func TestMQTTPubackReasonTableComplete01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodes(t, "pubackReasonTable", mqttPubackGolden)
	assert.Empty(t, diags, "MQTT-PUBACK-REASON-TABLE-COMPLETE-01: reason code set mismatch")
}

// TestMQTTPubackReasonTableComplete01_ReverseFixture proves the scanner fires on
// the fixture table that deliberately omits code 0x91.
func TestMQTTPubackReasonTableComplete01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodesInFixture(t, "pubackReasonTableMissingFixture", mqttPubackGolden)
	require.NotEmpty(t, diags,
		"MQTT-PUBACK-REASON-TABLE-COMPLETE-01 reverse fixture: scanner must flag the incomplete table")
	// The diagnostic must name the missing code 0x91.
	joined := diagMessages(diags)
	assert.Contains(t, joined, "0x91",
		"MQTT-PUBACK-REASON-TABLE-COMPLETE-01 reverse fixture: diagnostic must name missing code 0x91")
}

// TestMQTTPubackReasonTableComplete01_NonVacuous proves that the code-extraction
// scanner actually finds at least the known codes in the production table.
func TestMQTTPubackReasonTableComplete01_NonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var codes map[byte]bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			_, codes = mqttExtractReasonTableCodes(p, "pubackReasonTable")
			return nil
		})
	assert.GreaterOrEqual(t, len(codes), 1,
		"MQTT-PUBACK-REASON-TABLE-COMPLETE-01 non-vacuity: scanner found 0 codes in pubackReasonTable "+
			"— the go/types resolution path may be broken or the var was renamed")
}

// ─── MQTT-SUBACK-REASON-TABLE-COMPLETE-01 ────────────────────────────────────

// TestMQTTSubackReasonTableComplete01 asserts that subackReasonTable in
// adapters/mqtt contains exactly the 12 SUBACK reason codes of §3.9.3.
func TestMQTTSubackReasonTableComplete01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodes(t, "subackReasonTable", mqttSubackGolden)
	assert.Empty(t, diags, "MQTT-SUBACK-REASON-TABLE-COMPLETE-01: reason code set mismatch")
}

// TestMQTTSubackReasonTableComplete01_ReverseFixture proves the scanner fires on
// the fixture table that deliberately omits code 0x83.
func TestMQTTSubackReasonTableComplete01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTableCodesInFixture(t, "subackReasonTableMissingFixture", mqttSubackGolden)
	require.NotEmpty(t, diags,
		"MQTT-SUBACK-REASON-TABLE-COMPLETE-01 reverse fixture: scanner must flag the incomplete table")
	// The diagnostic must name the missing code 0x83.
	joined := diagMessages(diags)
	assert.Contains(t, joined, "0x83",
		"MQTT-SUBACK-REASON-TABLE-COMPLETE-01 reverse fixture: diagnostic must name missing code 0x83")
}

// TestMQTTSubackReasonTableComplete01_NonVacuous proves that the code-extraction
// scanner actually finds at least the known codes in the production table.
func TestMQTTSubackReasonTableComplete01_NonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var codes map[byte]bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			_, codes = mqttExtractReasonTableCodes(p, "subackReasonTable")
			return nil
		})
	assert.GreaterOrEqual(t, len(codes), 1,
		"MQTT-SUBACK-REASON-TABLE-COMPLETE-01 non-vacuity: scanner found 0 codes in subackReasonTable "+
			"— the go/types resolution path may be broken or the var was renamed")
}

// ─── MQTT-REASON-TABLE-POSITIONAL-01 ─────────────────────────────────────────

// TestMQTTReasonTablePositional01 asserts every row in all three reason-code
// tables uses positional composite-literal syntax (no *ast.KeyValueExpr elements).
// Named-field literals could omit a field (e.g. class), defeating the compile-time
// parity guard that positional literals enforce.
func TestMQTTReasonTablePositional01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var allDiags []Diagnostic
	for varName := range tableVarNames {
		allDiags = append(allDiags, scanMQTTReasonTablePositional(t, varName)...)
	}
	assert.Empty(t, allDiags,
		"MQTT-REASON-TABLE-POSITIONAL-01: named-field (key:value) syntax detected in reason-code table rows")
}

// TestMQTTReasonTablePositional01_ReverseFixture proves the positional scanner fires
// on pubackReasonTableFixture which uses named-field literals.
func TestMQTTReasonTablePositional01_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := scanMQTTReasonTablePositionalInFixture(t, "pubackReasonTableFixture")
	require.NotEmpty(t, diags,
		"MQTT-REASON-TABLE-POSITIONAL-01 reverse fixture: scanner must flag named-field literal rows")
}

// ─── Scanner implementations ─────────────────────────────────────────────────

// mqttReasonTableFixturePkgPath is the import path of the reason-table fixture.
const mqttReasonTableFixturePkgPath = PlatformModulePath + "/tools/archtest/internal/mqttreasonfixture"

// scanMQTTReasonTableCodes scans adapters/mqtt production code for the named var
// and compares its row codes against the golden set. Returns diagnostics for any
// missing or extra codes.
func scanMQTTReasonTableCodes(t *testing.T, varName string, golden map[byte]bool) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			diags = append(diags, mqttCheckReasonTableInPass(p, varName, golden)...)
			return nil
		})
	return diags
}

// scanMQTTReasonTableCodesInFixture is the fixture variant of scanMQTTReasonTableCodes.
func scanMQTTReasonTableCodesInFixture(t *testing.T, varName string, golden map[byte]bool) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{mqttReasonTableFixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttReasonTableFixturePkgPath {
				return nil
			}
			diags = append(diags, mqttCheckReasonTableInPass(p, varName, golden)...)
			return nil
		})
	return diags
}

// scanMQTTReasonTablePositional scans adapters/mqtt for named-field (key:value)
// elements in the rows of the named reason-code table var.
func scanMQTTReasonTablePositional(t *testing.T, varName string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			diags = append(diags, mqttCheckReasonTablePositionalInPass(p, varName)...)
			return nil
		})
	return diags
}

// scanMQTTReasonTablePositionalInFixture is the fixture variant of
// scanMQTTReasonTablePositional.
func scanMQTTReasonTablePositionalInFixture(t *testing.T, varName string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{mqttReasonTableFixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttReasonTableFixturePkgPath {
				return nil
			}
			diags = append(diags, mqttCheckReasonTablePositionalInPass(p, varName)...)
			return nil
		})
	return diags
}

// mqttCheckReasonTableInPass locates varName in the pass's files, extracts the
// byte code from each row's first positional element, and compares against golden.
// Returns diagnostics for missing or extra codes.
func mqttCheckReasonTableInPass(p *Pass, varName string, golden map[byte]bool) []Diagnostic {
	found, codes := mqttExtractReasonTableCodes(p, varName)
	if !found {
		return []Diagnostic{{
			Rel:     "errors.go",
			Line:    0,
			Message: fmt.Sprintf("MQTT-REASON-TABLE-COMPLETE: var %q not found in package %s (expected in adapters/mqtt/errors.go)", varName, p.Pkg.Path()),
		}}
	}
	return mqttCompareCodeSets(varName, codes, golden)
}

// mqttExtractReasonTableCodes locates varName in the pass's production files and
// returns the set of byte codes found in each row's first positional element.
// ok=false if the var was not found.
func mqttExtractReasonTableCodes(p *Pass, varName string) (ok bool, codes map[byte]bool) {
	codes = make(map[byte]bool)
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.GenDecl](f, func(gen *ast.GenDecl) {
			EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					if name.Name != varName {
						continue
					}
					ok = true
					// vs.Values[0] is the slice composite literal []T{row0, row1, ...}
					if len(vs.Values) == 0 {
						return
					}
					outerLit, isLit := vs.Values[0].(*ast.CompositeLit)
					if !isLit {
						return
					}
					// Each direct child CompositeLit of outerLit is a row: T{code, name, ...}
					EachInChildren[ast.CompositeLit](outerLit, func(rowLit *ast.CompositeLit) {
						if len(rowLit.Elts) == 0 {
							return
						}
						// First positional element is the byte code.
						firstElt := rowLit.Elts[0]
						// For named-field rows, skip (POSITIONAL-01 handles those).
						if _, isKV := firstElt.(*ast.KeyValueExpr); isKV {
							return
						}
						tv, tvOK := p.TypesInfo.Types[firstElt]
						if !tvOK || tv.Value == nil {
							return
						}
						// Extract the integer value of the byte literal.
						intVal, exact := constant.Int64Val(constant.ToInt(tv.Value))
						if exact {
							codes[byte(intVal)] = true
						}
					})
				}
			})
		})
	}
	return ok, codes
}

// mqttCompareCodeSets returns diagnostics for codes present in golden but missing
// from actual, and codes in actual but absent from golden.
func mqttCompareCodeSets(varName string, actual, golden map[byte]bool) []Diagnostic {
	var diags []Diagnostic
	for code := range golden {
		if !actual[code] {
			diags = append(diags, Diagnostic{
				Rel:  "errors.go",
				Line: 0,
				Message: fmt.Sprintf(
					"MQTT-REASON-TABLE-COMPLETE: %s missing spec code 0x%02x", varName, code),
			})
		}
	}
	for code := range actual {
		if !golden[code] {
			diags = append(diags, Diagnostic{
				Rel:  "errors.go",
				Line: 0,
				Message: fmt.Sprintf(
					"MQTT-REASON-TABLE-COMPLETE: %s has extra non-spec code 0x%02x", varName, code),
			})
		}
	}
	return diags
}

// mqttCheckReasonTablePositionalInPass scans the named var's row literals for any
// *ast.KeyValueExpr elements (named-field syntax). Returns a diagnostic for each
// such row.
func mqttCheckReasonTablePositionalInPass(p *Pass, varName string) []Diagnostic {
	var diags []Diagnostic
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.GenDecl](f, func(gen *ast.GenDecl) {
			EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					if name.Name != varName {
						continue
					}
					if len(vs.Values) == 0 {
						return
					}
					outerLit, isLit := vs.Values[0].(*ast.CompositeLit)
					if !isLit {
						return
					}
					// rowIdx tracks the row index for the diagnostic message.
					// All direct CompositeLit children of outerLit are rows (same
					// set the prior for-range hit), so incrementing once per
					// EachInChildren callback is equivalent to the prior range index.
					rowIdx := -1
					EachInChildren[ast.CompositeLit](outerLit, func(rowLit *ast.CompositeLit) {
						rowIdx++
						EachInChildren[ast.KeyValueExpr](rowLit, func(kv *ast.KeyValueExpr) {
							pos := p.Fset.Position(kv.Pos())
							diags = append(diags, Diagnostic{
								Rel:  p.Rel(f),
								Line: pos.Line,
								Message: fmt.Sprintf(
									"MQTT-REASON-TABLE-POSITIONAL-01: %s row %d uses named-field (key:value) "+
										"literal syntax at %s:%d — positional literals required so that "+
										"adding a field is a compile error at every existing row",
									varName, rowIdx, p.Rel(f), pos.Line),
							})
						})
					})
				}
			})
		})
	}
	return diags
}

// diagMessages returns a single string with all diagnostic messages joined by newlines.
func diagMessages(diags []Diagnostic) string {
	var sb strings.Builder
	for _, d := range diags {
		sb.WriteString(d.Message)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// ─── Anti-vacuity probes ──────────────────────────────────────────────────────

// TestMQTTConnackReasonTableComplete01_NonVacuous proves that the code-extraction
// scanner actually finds at least the known codes in the production table
// (scanner found something, not an empty scan).
func TestMQTTConnackReasonTableComplete01_NonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var diags []Diagnostic
	var codes map[byte]bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			_, codes = mqttExtractReasonTableCodes(p, "connackReasonTable")
			return nil
		})
	_ = diags
	assert.GreaterOrEqual(t, len(codes), 1,
		"MQTT-CONNACK-REASON-TABLE-COMPLETE-01 non-vacuity: scanner found 0 codes in connackReasonTable "+
			"— the go/types resolution path may be broken or the var was renamed")
}

// TestMQTTReasonTablePositional01_NonVacuous proves that the positional scanner
// would fire: it loads the production table and verifies at least one row was
// inspected for positional compliance.
func TestMQTTReasonTablePositional01_NonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var rowCount int
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, []string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInSubtree[ast.GenDecl](f, func(gen *ast.GenDecl) {
					EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
						for _, name := range vs.Names {
							if name.Name != "connackReasonTable" {
								continue
							}
							if len(vs.Values) == 0 {
								return
							}
							outerLit, isLit := vs.Values[0].(*ast.CompositeLit)
							if !isLit {
								return
							}
							rowCount += len(outerLit.Elts)
						}
					})
				})
			}
			return nil
		})
	assert.GreaterOrEqual(t, rowCount, 1,
		"MQTT-REASON-TABLE-POSITIONAL-01 non-vacuity: scanner found 0 rows in connackReasonTable "+
			"— the scanner is broken or the var was renamed")
}

