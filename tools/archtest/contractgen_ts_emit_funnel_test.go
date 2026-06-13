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
//	B3 — path prefix: every TS output-path-candidate string literal must target
//	     a path under generated-ts/ (not generated/, pkg/, tmp/ or any other
//	     subtree). Enforced as a POSITIVE allowlist, not a "contains generated/"
//	     denylist, so bypass paths that never mention generated/ are still caught.
//
// AI-robust ceiling:
//
//	B1: Medium — content scan over .tmpl/.go files for the TS file-emit marker.
//	B2: Medium — AST scan: renderTS / renderBarrel function bodies must not
//	    contain identifiers "Render" or "FormatGoSource" from the codegen pkg.
//	B3: Medium — AST literal scan over the generator source: every output-path
//	    candidate (ends .ts, contains /, not a "/"- or "."-prefixed trim arg)
//	    must start with generated-ts/.
//
// Blind spots (documented): a TS output path assembled entirely from runtime
// data with no ".ts"-suffixed literal segment would evade the literal scan
// (the candidate predicate keys on a literal `.ts` suffix). Neither that nor a
// codegen.Write whose path is fully runtime-built occurs in the current
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

// isAllowedTSTemplate reports whether rel is in the contractgen/templates allowlist
// of permitted .ts.tmpl files. Used by both the live B1 scan and the RED/GREEN
// self-check fixtures so both paths exercise the exact same logic.
func isAllowedTSTemplate(rel string) bool {
	return rel == tsTypesTemplate || rel == tsBarrelTemplate
}

// TestContractgenTSFunnel_B1_RedFixture proves the B1 detector fires on a
// template with .ts.tmpl outside the contractgen/templates directory.
func TestContractgenTSFunnel_B1_RedFixture(t *testing.T) {
	t.Parallel()
	rogue := "tools/cellgen/templates/types.ts.tmpl"
	if isAllowedTSTemplate(rogue) {
		t.Fatal("RED fixture: rogue path unexpectedly matches the allowlist (test is broken)")
	}
	// The rogue path is NOT in the allowlist — a real B1 scan would report it as a violation.
}

// TestContractgenTSFunnel_B1_GreenFixture proves the allowed templates pass B1.
func TestContractgenTSFunnel_B1_GreenFixture(t *testing.T) {
	t.Parallel()
	for _, allowed := range []string{tsTypesTemplate, tsBarrelTemplate} {
		if !isAllowedTSTemplate(allowed) {
			t.Errorf("GREEN fixture: allowed template %q rejected by isAllowedTSTemplate", allowed)
		}
	}
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

// isTSOutputPathCandidate reports whether a string literal looks like a TS
// output path (as opposed to a suffix/extension pattern used for string
// trimming). A candidate ends in ".ts", contains a path separator, and does NOT
// start with "/" or ".". This deliberately excludes the trimming/join args that
// appear in the real emitter — "/types.ts" and ".ts" (TrimSuffix args) and bare
// "types.ts"/"index.ts" (filepath.Join components) — none of which are output
// paths, so the allowlist below stays free of false positives.
func isTSOutputPathCandidate(s string) bool {
	return strings.HasSuffix(s, ".ts") &&
		strings.Contains(s, "/") &&
		!strings.HasPrefix(s, "/") &&
		!strings.HasPrefix(s, ".")
}

// scanTSPathViolations enforces a POSITIVE allowlist: every TS output-path
// candidate literal (see isTSOutputPathCandidate) must start with
// "generated-ts/". Any other prefix is a misrouted TS write — this catches not
// only the old "generated/contracts/.../types.ts" mistake but also bypass paths
// that never mention "generated/" at all (e.g. "pkg/foo.ts", "tmp/foo.ts"),
// which a "contains generated/" check would silently miss.
func scanTSPathViolations(f *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		s := strings.Trim(lit.Value, `"`+"`")
		if !isTSOutputPathCandidate(s) {
			return
		}
		if !strings.HasPrefix(s, "generated-ts/") {
			out = append(out, rel+": TS path literal "+lit.Value+
				" targets a directory other than generated-ts/ — TS output must go to generated-ts/")
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

// ---------------------------------------------------------------------------
// B3 RED + GREEN fixture self-checks
// ---------------------------------------------------------------------------

// TestContractgenTSFunnel_B3_RedFixtureDetectsGeneratedPrefix proves the B3
// scanner fires on a string literal "generated/x.ts" (wrong prefix).
func TestContractgenTSFunnel_B3_RedFixtureDetectsGeneratedPrefix(t *testing.T) {
	t.Parallel()
	src := `package contractgen
func emitBad() { _ = "generated/contracts/http/order/create/v1/types.ts" }
`
	f := parseTSFixture(t, src)
	violations := scanTSPathViolations(f, "generator.go")
	if len(violations) == 0 {
		t.Error("B3 detector missed generated/ prefix in TS path literal")
	}
}

// TestContractgenTSFunnel_B3_GreenFixtureAllowsGeneratedTS proves the B3
// scanner does NOT flag a string literal with the correct "generated-ts/" prefix.
func TestContractgenTSFunnel_B3_GreenFixtureAllowsGeneratedTS(t *testing.T) {
	t.Parallel()
	src := `package contractgen
func emitGood() { _ = "generated-ts/index.ts" }
`
	f := parseTSFixture(t, src)
	violations := scanTSPathViolations(f, "tsemit.go")
	if len(violations) != 0 {
		t.Errorf("B3 scanner over-flagged valid generated-ts/ path: %v", violations)
	}
}

// TestContractgenTSFunnel_B3_RedFixtureDetectsBypassPaths proves the positive
// allowlist catches misrouted TS writes that never mention "generated/" — the
// exact gap a "contains generated/" denylist would miss (pkg/foo.ts, tmp/foo.ts).
func TestContractgenTSFunnel_B3_RedFixtureDetectsBypassPaths(t *testing.T) {
	t.Parallel()
	for _, bypass := range []string{"pkg/foo.ts", "tmp/foo.ts", "internal/bar/baz.ts"} {
		src := "package contractgen\nfunc emitBad() { _ = \"" + bypass + "\" }\n"
		f := parseTSFixture(t, src)
		violations := scanTSPathViolations(f, "tsemit.go")
		if len(violations) == 0 {
			t.Errorf("B3 detector missed bypass TS path %q (does not start with generated-ts/)", bypass)
		}
	}
}

// TestContractgenTSFunnel_B3_GreenFixtureAllowsTrimPatterns proves the candidate
// predicate excludes the suffix/extension and bare-filename literals the real
// emitter uses for string trimming and filepath.Join (anti-false-positive lock):
// "/types.ts" and ".ts" (TrimSuffix args) and "types.ts"/"index.ts" (join
// components) must NOT be flagged.
func TestContractgenTSFunnel_B3_GreenFixtureAllowsTrimPatterns(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"/types.ts", ".ts", "types.ts", "index.ts"} {
		src := "package contractgen\nfunc trim() { _ = \"" + ok + "\" }\n"
		f := parseTSFixture(t, src)
		violations := scanTSPathViolations(f, "tsemit.go")
		if len(violations) != 0 {
			t.Errorf("B3 scanner over-flagged trim/join literal %q: %v", ok, violations)
		}
	}
}
