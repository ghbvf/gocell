//go:build archtest

// INVARIANT: PROJECTION-TOMAP-FULL-COLUMN-SET-01
//
// This file owns ONE invariant: the generated `ToMap()` of every
// responseProjection resource-item DTO emits the FULL, STABLE column set —
// one map entry per struct field, ALWAYS present, with NO conditional
// (omitempty-style) omission. The column-masking funnel
// (projection.NewProjection / NewProjectionList) can only redact a key it can
// SEE (applyMask is a no-op for an absent key), so a uniform always-present
// column set is what closes the presence-based side channel that
// ADR 202606112000-1350 Decision 2 exists to close: field *presence* must never
// reveal whether a masked column held data.
//
// History (why this guard exists): #1350 (PR-12) shipped ToMap emitting the full
// column set per Decision 2. #2159 then re-introduced omitempty fission into
// ToMap ("align the projection path with struct json.Marshal") — re-opening the
// exact presence side channel on the masked diagnostic columns
// (correlationId/traceId/subjectId) — WITHOUT amending the ADR, because the
// Decision-2 invariant lived only as ADR prose (Soft) with no machine guard.
// This archtest is that machine guard (#1875).
//
// # AI-robust rating (charter §分级)
//
//   - Medium (type-aware AST scan, primary guard). For every generated ToMap
//     method this rule pins the body shape to a single
//     `return map[string]any{ <one entry per field> }` literal with NO IfStmt.
//     A #2159-style regression (required fields in the literal + optional fields
//     added under `if i.X != "" { … }`) trips it two ways: the body is no longer
//     a single return-literal, and it contains IfStmt(s). This is STRICTLY
//     STRONGER than the golden byte-lock backstop: even if a regressor
//     `-update`s the goldens to match an omitempty body (exactly what #2159 did),
//     this semantic shape assertion still fails. The golden byte-lock (Hard, on
//     output drift) and this rule (Medium, on semantic shape) are the two legs;
//     the prose ADR alone was the Soft gap that #2159 slipped through.
//   - Known Hard ceiling (deliberately not built here, charter §优雅简洁): a
//     load-bearing runtime check inside the masking funnel — NewProjectionList
//     fail-closed unless each row's key set equals a generated expected-column
//     constant — would make omission unrepresentable at the PEP. It is not added
//     because it couples the funnel to per-contract column constants for a
//     property this static scan already covers; revisit if a non-AST emission
//     path for the column map ever appears.
//
// # Tool blind spots (charter §强制盲区自检)
//
//   - Scope is the generated ToMap body only. A hand-written column map built
//     OUTSIDE ToMap (no DTO does this today — RESOURCE-PROJECTION-CALLSITE-LOCK-01
//     pins Response.Data to the sealed carrier whose sole populator is ToMap) is
//     not seen.
//   - It counts entries == struct field count; it does not verify each entry's
//     KEY equals the field's wire tag (that key↔tag fidelity is covered by the
//     contractgen golden + the ToMap godoc). A literal with the right COUNT but a
//     duplicated/wrong key would pass here — but such a literal is a contractgen
//     template bug the goldens catch.
//   - Anti-vacuity: minExpectedToMapDTOs is the non-empty floor. If the generated
//     tree is missing (zero ToMap methods loaded) the production test FAILS rather
//     than passing vacuously. The reverse self-check (RED/GREEN fixture) proves
//     the detector genuinely distinguishes a full-set literal from an
//     omitempty-fission body.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const tomapFullColumnSetFixturePkg = "./tools/archtest/internal/tomapfullcolumnsetfixture"

// minExpectedToMapDTOs is the anti-vacuity floor: the number of generated
// responseProjection resource-item DTOs that carry a ToMap method today (audit
// list/get, auth role.list/role.check/setup.status/user.get, config
// get/list/internalapi/flags.get/flags.list, policy get/list, devicestate,
// devicecompliance, deviceidentity.status = 16). A drop below this floor means
// the generated tree was not loaded (vacuous pass) — fail instead.
const minExpectedToMapDTOs = 16

// tomapFixtureGreen / tomapFixtureRed name the exported fixture DTO types the
// reverse self-check inspects: green ToMaps emit the full column set as a single
// literal; red ToMaps omit columns (omitempty fission / short literal).
var (
	tomapFixtureGreen = []string{"GoodItem"}
	tomapFixtureRed   = []string{"BadOmitItem", "BadShortLiteralItem"}
)

// TestProjectionToMapFullColumnSet01 asserts every generated ToMap method emits
// the full, stable column set with no conditional omission.
func TestProjectionToMapFullColumnSet01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	count := 0

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./generated/contracts/http/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				scanner.EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if !isToMapMethod(fn) {
						return
					}
					count++
					diags = append(diags, toMapFullColumnSetDiags(p, f, fn)...)
				})
			}
			return nil
		})

	if count < minExpectedToMapDTOs {
		t.Fatalf("PROJECTION-TOMAP-FULL-COLUMN-SET-01: found %d generated ToMap methods, expected >= %d — the "+
			"generated tree was likely not loaded, which would make the rule pass vacuously. Run "+
			"`gocell generate contract --all`.", count, minExpectedToMapDTOs)
	}

	Report(t, "PROJECTION-TOMAP-FULL-COLUMN-SET-01", diags)
}

// TestProjectionToMapFullColumnSet01_ScannerCatchesViolation is the reverse
// self-check: it runs the SAME detector over the build-tagged RED/GREEN fixture
// and asserts it flags exactly the omitting ToMaps and none of the full-set
// control(s) — proving the detector is not vacuous.
func TestProjectionToMapFullColumnSet01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	greenSeen := map[string]bool{}
	redSeen := map[string]bool{}
	var greenDiags []Diagnostic

	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{tomapFullColumnSetFixturePkg}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				scanner.EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if !isToMapMethod(fn) {
						return
					}
					recv := toMapReceiverName(fn)
					d := toMapFullColumnSetDiags(p, f, fn)
					switch {
					case containsString(tomapFixtureGreen, recv):
						greenSeen[recv] = true
						greenDiags = append(greenDiags, d...)
					case containsString(tomapFixtureRed, recv):
						if len(d) > 0 {
							redSeen[recv] = true
						}
					}
				})
			}
			return nil
		})

	if len(greenDiags) != 0 {
		t.Errorf("PROJECTION-TOMAP-FULL-COLUMN-SET-01 self-check: detector over-flagged a full-set control: %+v", greenDiags)
	}
	for _, name := range tomapFixtureGreen {
		if !greenSeen[name] {
			t.Fatalf("PROJECTION-TOMAP-FULL-COLUMN-SET-01 self-check: green control %q was not inspected", name)
		}
	}
	for _, name := range tomapFixtureRed {
		if !redSeen[name] {
			t.Fatalf("PROJECTION-TOMAP-FULL-COLUMN-SET-01 self-check: detector failed to flag the omitting fixture %q", name)
		}
	}
}

// isToMapMethod reports whether fn is a `func (i T) ToMap() ...` method.
func isToMapMethod(fn *ast.FuncDecl) bool {
	return fn != nil && fn.Recv != nil && len(fn.Recv.List) == 1 && fn.Name != nil && fn.Name.Name == "ToMap"
}

// toMapReceiverName returns the receiver TYPE name of a ToMap method (value or
// pointer receiver), or "" when it cannot be resolved.
func toMapReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return ""
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// toMapFullColumnSetDiags is the SINGLE detector shared by the production test
// and the fixture reverse self-check. It returns a diagnostic unless fn's body
// is exactly `return map[string]any{ <one entry per struct field> }` with no
// conditional (IfStmt) omission.
func toMapFullColumnSetDiags(p *Pass, f *ast.File, fn *ast.FuncDecl) []Diagnostic {
	rel := p.Rel(f)
	line := p.Fset.Position(fn.Pos()).Line
	recv := toMapReceiverName(fn)

	diag := func(msg string) []Diagnostic {
		return []Diagnostic{{Rel: rel, Line: line, Message: fmt.Sprintf(
			"PROJECTION-TOMAP-FULL-COLUMN-SET-01: %s.ToMap %s — it MUST emit the full column set as a single "+
				"`return map[string]any{ <one entry per field> }` literal (every column always present), so the "+
				"masking funnel can redact any column and field presence never leaks whether a masked column held "+
				"data (ADR 202606112000-1350 Decision 2). A conditional/omitempty body re-opens the side channel "+
				"(#2159 → #1875).", recv, msg)}}
	}

	if fn.Body == nil {
		return diag("has no body")
	}
	// No conditional omission anywhere in the body.
	hasIf := false
	scanner.EachInSubtree[ast.IfStmt](fn.Body, func(*ast.IfStmt) {
		hasIf = true
	})
	if hasIf {
		return diag("uses a conditional (`if`) to add columns — omitempty fission")
	}
	// Body must be exactly one `return <composite literal>`.
	if len(fn.Body.List) != 1 {
		return diag(fmt.Sprintf("body has %d statements, want exactly one `return map[string]any{…}`", len(fn.Body.List)))
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return diag("body is not a single return statement")
	}
	lit, ok := ret.Results[0].(*ast.CompositeLit)
	if !ok {
		return diag("does not return a map literal directly")
	}
	if _, ok := lit.Type.(*ast.MapType); !ok {
		return diag("return value is not a map[string]any literal")
	}
	want := structFieldCount(p.Pkg, recv)
	if want < 0 {
		return diag(fmt.Sprintf("receiver type %q not found in package types", recv))
	}
	if len(lit.Elts) != want {
		return diag(fmt.Sprintf("emits %d columns but the struct has %d fields — every field must be present",
			len(lit.Elts), want))
	}
	return nil
}

// structFieldCount returns the number of fields of the named struct in pkg, or
// -1 when the type is absent / not a struct.
func structFieldCount(pkg *types.Package, name string) int {
	if pkg == nil {
		return -1
	}
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		return -1
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return -1
	}
	return st.NumFields()
}
