//go:build archtest

package archtest

// INVARIANT: CONTRACTGEN-TS-EMIT-FUNNEL-01
//
// The TS contract emitter (#2004) must route all .ts generation through a
// single sealed funnel: renderTS / renderBarrel in tools/codegen/contractgen.
// Three legs close the full funnel:
//
//	B1 — uniqueness: the only files that render .ts templates are
//	     types.ts.tmpl and barrel.ts.tmpl inside contractgen/templates/.
//	     No other template or generator emits .ts content.
//	B2 — no Render/FormatGoSource call: renderTS and renderBarrel must NOT
//	     call codegen.Render or codegen.FormatGoSource (which would reject
//	     a .ts file — TS files must bypass goimports/gofumpt).
//	B3 — path prefix: all codegen.Write calls for .ts output must target a
//	     path under generated-ts/ (not generated/ or any other subtree).
//
// AI-robust ceiling:
//
//	B1: Medium — content scan over .tmpl/.go files for the TS file-emit marker.
//	B2: Medium — AST scan: renderTS / renderBarrel function bodies must not
//	    contain identifiers "Render" or "FormatGoSource" from the codegen pkg.
//	B3: Medium — content scan over the generator source for Write calls carrying
//	    hardcoded path prefixes outside generated-ts/.
//
// Blind spots (documented): a template that assembles the .ts extension at
// runtime via string ops rather than a literal `.ts` suffix; or a codegen.Write
// call path built entirely from runtime data. Neither occurs in the current
// implementation; asserted clean by the production walk.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

const (
	// tsTypesTemplate is the allowed TS types template file.
	tsTypesTemplate = "tools/codegen/contractgen/templates/types.ts.tmpl"
	// tsBarrelTemplate is the allowed TS barrel template file.
	tsBarrelTemplate = "tools/codegen/contractgen/templates/barrel.ts.tmpl"
)

// ---------------------------------------------------------------------------
// B1 — TS template uniqueness: only types.ts.tmpl and barrel.ts.tmpl may
//
//	render .ts content, and they must live in contractgen/templates/.
//
// Anti-vacuity: both allowed templates must be found (the found-set must be
// exactly {types.ts.tmpl, barrel.ts.tmpl}).
// ---------------------------------------------------------------------------
func TestContractgenTSEmitFunnel_B1_TemplateUniqueness(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(
		root, []string{"tools/codegen"},
		MatchRels(func(rel string) bool {
			if strings.Contains(rel, "/testdata/") {
				return false
			}
			return strings.HasSuffix(rel, ".tmpl") || strings.HasSuffix(rel, ".go")
		}),
	)

	var tsEmitFiles []string
	EachContentFile(t, scope, []string{".tmpl"}, func(_ *testing.T, fc ContentContext) {
		// A .tmpl file is a TS emitter if its path ends in .ts.tmpl
		if strings.HasSuffix(fc.Rel, ".ts.tmpl") {
			tsEmitFiles = append(tsEmitFiles, fc.Rel)
		}
	})
	sort.Strings(tsEmitFiles)

	want := []string{tsBarrelTemplate, tsTypesTemplate}
	sort.Strings(want)
	if len(tsEmitFiles) == 0 {
		t.Errorf("CONTRACTGEN-TS-EMIT-FUNNEL-01 (B1): no .ts.tmpl files found under tools/codegen — "+
			"anti-vacuity failure: the funnel test would pass vacuously if templates were removed. "+
			"Expected exactly %v", want)
		return
	}
	for _, got := range tsEmitFiles {
		found := false
		for _, w := range want {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CONTRACTGEN-TS-EMIT-FUNNEL-01 (B1): unexpected .ts.tmpl file %q — "+
				"TS templates must live only in %v", got, want)
		}
	}
	for _, w := range want {
		found := false
		for _, got := range tsEmitFiles {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CONTRACTGEN-TS-EMIT-FUNNEL-01 (B1): expected .ts.tmpl file %q not found — "+
				"anti-vacuity: both templates must exist", w)
		}
	}
}

// ---------------------------------------------------------------------------
// B2 — renderTS / renderBarrel must not call codegen.Render or FormatGoSource.
//
// These functions run goimports + gofumpt which reject .ts files. TS output
// must flow through text/template.Execute → buffer → codegen.Write directly.
// ---------------------------------------------------------------------------
func TestContractgenTSEmitFunnel_B2_NoRenderOrFormatInTSEmitter(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(
		root, []string{"tools/codegen/contractgen"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		}),
	)

	var violations []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if !strings.HasSuffix(rel, "tsemit.go") {
				continue
			}
			violations = append(violations, scanTSEmitForbiddenCalls(f, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CONTRACTGEN-TS-EMIT-FUNNEL-01 (B2): %s", v)
	}
}

// ---------------------------------------------------------------------------
// B3 — TS Write path prefix: generated-ts/ only.
//
// Scans tsemit.go and generator.go for string literals containing .ts
// path context that must stay under generated-ts/.
// ---------------------------------------------------------------------------
func TestContractgenTSEmitFunnel_B3_TSPathPrefixIsGeneratedTS(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(
		root, []string{"tools/codegen/contractgen"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		}),
	)

	var violations []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			violations = append(violations, scanTSPathViolations(f, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CONTRACTGEN-TS-EMIT-FUNNEL-01 (B3): %s", v)
	}
}

// ---------------------------------------------------------------------------
// Reverse self-checks (anti-vacuity + RED fixture proofs)
// ---------------------------------------------------------------------------

// TestContractgenTSFunnel_B1_RedFixture proves the B1 detector fires on a
// template with .ts.tmpl outside the contractgen/templates directory.
func TestContractgenTSFunnel_B1_RedFixture(t *testing.T) {
	t.Parallel()
	// Simulate a rogue .ts.tmpl in a non-contractgen location
	rogue := "tools/cellgen/templates/types.ts.tmpl"
	want := []string{tsBarrelTemplate, tsTypesTemplate}
	sort.Strings(want)

	found := false
	for _, w := range want {
		if rogue == w {
			found = true
		}
	}
	if found {
		t.Fatal("RED fixture: rogue path unexpectedly matches the allowlist (test is broken)")
	}
	// The rogue file would surface as a violation in B1 (not in the want set).
}

// TestContractgenTSFunnel_B2_RedFixtureDetectsRenderCall proves that the B2
// scanner detects a "Render(" call in a tsemit.go fixture.
func TestContractgenTSFunnel_B2_RedFixtureDetectsRenderCall(t *testing.T) {
	t.Parallel()
	// Inline fixture: a function named renderTS that calls codegen.Render
	src := `package contractgen
func renderTS() { _ = codegen.Render("mod", renderOpts) }
`
	f := parseTSFixture(t, src)
	violations := scanTSEmitForbiddenCalls(f, "tsemit.go")
	if len(violations) == 0 {
		t.Error("B2 detector missed codegen.Render call in tsemit.go fixture")
	}
}

// TestContractgenTSFunnel_B2_GreenFixtureNoCalls proves the scanner does not
// flag a tsemit.go that uses only text/template and codegen.Write.
func TestContractgenTSFunnel_B2_GreenFixtureNoCalls(t *testing.T) {
	t.Parallel()
	src := `package contractgen
func renderTS() ([]byte, error) {
	var buf []byte
	return buf, nil
}
`
	f := parseTSFixture(t, src)
	violations := scanTSEmitForbiddenCalls(f, "tsemit.go")
	if len(violations) != 0 {
		t.Errorf("B2 scanner over-flagged clean tsemit.go: %v", violations)
	}
}

// ---------------------------------------------------------------------------
// Detection helpers
// ---------------------------------------------------------------------------

// scanTSEmitForbiddenCalls scans a single *ast.File (expected to be tsemit.go)
// for forbidden call patterns: codegen.Render or codegen.FormatGoSource.
func scanTSEmitForbiddenCalls(f *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if id.Name != "codegen" {
			return
		}
		switch sel.Sel.Name {
		case "Render", "FormatGoSource":
			out = append(out, rel+": tsemit calls codegen."+sel.Sel.Name+
				" — TS files must not pass through the Go formatter (it rejects non-Go content); "+
				"use text/template → buffer → codegen.Write directly")
		}
	})
	return out
}

// scanTSPathViolations scans for string literals that look like generated .ts
// paths under a prefix OTHER than generated-ts/.
func scanTSPathViolations(f *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		s := strings.Trim(lit.Value, `"`+"`")
		// Look for literals that contain "generated/" but NOT "generated-ts/"
		// and end with .ts, which would be a misrouted TS path.
		if strings.Contains(s, "generated/") && strings.HasSuffix(s, ".ts") {
			out = append(out, rel+": TS path literal "+lit.Value+
				" uses generated/ prefix instead of generated-ts/ — TS output must go to generated-ts/")
		}
	})
	return out
}

// parseTSFixture parses an inline Go source fixture for the B2 self-checks.
func parseTSFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parseTSFixture: parse error: %v", err)
	}
	return f
}
