package archtest

// parser_matcher_examples_symmetry.go — importable
// PARSER-MATCHER-EXAMPLES-SYMMETRY-01 rule logic (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run it; Go never
// compiles a dependency's _test.go. GoCell's own Test* in
// parser_matcher_examples_symmetry_test.go dogfood the same Check* (single
// source, no parallel rule body).
//
// # PARSER-MATCHER-EXAMPLES-SYMMETRY-01
//
// Every match{Kind}Path function in kernel/metadata/locator_conventional.go
// must accept both the root form (<root>/<kind>/...) and the examples form
// (examples/*/<kind>/...). The archtest uses typed AST const evaluation to
// walk each matcher body (and its directly-called package-private helper
// functions within the same file), extract every `parts[N] == <CONST>`
// BinaryExpr where the RHS is a const-resolved string, and assert the set of
// values appearing in parts[0] comparisons includes both the kind's root
// segment literal AND the literal "examples".
//
// # AI-robust grade
//
// Medium. Two layers:
//
//  1. Primary symmetry check (downstream): typed AST + EvaluateConstString
//     (resolves untyped const, cross-package Ident, const string concat). Not
//     a string anchor; robust to whitespace/identifier-rename refactors but
//     fails immediately if a matcher omits examples/ support.
//
//  2. Upstream coverage check: after the main test loop,
//     CheckParserMatcherExamplesSymmetry01 iterates all FuncDecls in parser.go
//     matching ^match.+Path$ and asserts each name appears as a key in
//     matcherRootSegment. This closes the gap where a newly added match*Path
//     function could silently bypass the primary check by simply being absent
//     from the hand-maintained map.
//
// # Not registered (register=none)
//
// Not registered in StandardCellRules: gocell-internal-layout funnel — the
// rule scans kernel/metadata/locator_conventional.go which does not exist in
// an external module. Running against an external repo produces vacuous-green
// or false-red. Enforced in GoCell via TestParserMatcherExamplesSymmetry01.

import (
	"go/ast"
	"go/token"
	"regexp"
	"testing"
)

// matcherRootSegment maps each match*Path function name (in
// kernel/metadata/locator_conventional.go, post-M1 Locator funnel) to the
// expected root segment literal for the non-examples form. E.g.
// matchAssemblyPath → "assemblies". Before M1 these matchers lived in
// kernel/metadata/locator_conventional.go as match*YAML; the rename + file
// move accompanied the LOCATOR-DISCOVERY-FUNNEL-01 refactor (#1082).
var matcherRootSegment = map[string]string{
	"matchCellPath":     "cells",
	"matchSlicePath":    "cells",
	"matchContractPath": "contracts",
	"matchJourneyPath":  "journeys",
	"matchAssemblyPath": "assemblies",
}

var matchYAMLFuncRE = regexp.MustCompile(`^match.+Path$`)

// parserMatcherMetadataPkg is the import path of the kernel/metadata package.
// Derived from PlatformModulePath so a module rename updates exactly one place
// (ARCHTEST-MODULE-PATH-FUNNEL-01).
const parserMatcherMetadataPkg = PlatformModulePath + "/kernel/metadata"

// parserMatcherLocatorFile is the module-relative path of the file holding
// the match*Path functions.
const parserMatcherLocatorFile = "kernel/metadata/locator_conventional.go"

// matcherAndCalleeBodies returns the function body block-statements that
// should be inspected for blind-spot probes — the matcher itself plus its
// direct callees within the same file. This mirrors the scope used by the
// main check (matcher body + collectCalleeNames + callee body expansion),
// ensuring that blind-spot probes cover exactly the same AST surface as the
// primary invariant check.
//
// The caller walks each returned *ast.BlockStmt independently using
// EachInSubtree[ast.X](body, ...).
func matcherAndCalleeBodies(file *ast.File, matcher *ast.FuncDecl) []*ast.BlockStmt {
	// Build a local map of function name → FuncDecl for all functions in the file.
	fileFuncs := make(map[string]*ast.FuncDecl)
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Name != nil {
			fileFuncs[fn.Name.Name] = fn
		}
	})

	bodies := []*ast.BlockStmt{matcher.Body}

	// Walk the matcher body for direct CallExpr calls (one level deep).
	EachInSubtree[ast.CallExpr](matcher.Body, func(ce *ast.CallExpr) {
		id, ok := ce.Fun.(*ast.Ident)
		if !ok {
			return
		}
		if callee, found := fileFuncs[id.Name]; found && callee.Body != nil {
			bodies = append(bodies, callee.Body)
		}
	})

	return bodies
}

// matcherResult holds per-matcher symmetry findings. line is the 1-based line of
// the matcher FuncDecl in parserMatcherLocatorFile, used to anchor symmetry
// diagnostics to a clickable position.
type matcherResult struct {
	foundExamples bool
	foundRoot     bool
	line          int
}

// CheckParserMatcherExamplesSymmetry01 runs PARSER-MATCHER-EXAMPLES-SYMMETRY-01
// over the running module and returns diagnostics. It does NOT call t.Errorf;
// the caller should funnel results through
// Report(t, "PARSER-MATCHER-EXAMPLES-SYMMETRY-01", ...).
//
// Not registered in StandardCellRules: gocell-internal-layout funnel; vacuous-
// green/false-red in an external module; enforced in GoCell via
// TestParserMatcherExamplesSymmetry01.
func CheckParserMatcherExamplesSymmetry01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	results := make(map[string]*matcherResult)

	runPrimarySymmetryCheck(t, results)

	var diags []Diagnostic
	diags = append(diags, collectSymmetryDiagnostics(results)...)
	diags = append(diags, runUpstreamCoverageCheck(t)...)
	return diags
}

// runPrimarySymmetryCheck fills results with per-matcher foundExamples/foundRoot.
func runPrimarySymmetryCheck(t *testing.T, results map[string]*matcherResult) {
	t.Helper()
	_ = Run(t, Typed(TypedOpts{}, []string{"./kernel/metadata/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != parserMatcherMetadataPkg {
			return nil
		}
		parserFuncs := collectParserFuncs(p)
		scanMatcherSymmetry(p, parserFuncs, results)
		return nil
	})
}

// collectParserFuncs returns a map of function name → FuncDecl for all
// functions declared in parserMatcherLocatorFile.
func collectParserFuncs(p *Pass) map[string]*ast.FuncDecl {
	parserFuncs := make(map[string]*ast.FuncDecl)
	for _, f := range p.Files {
		if p.Rel(f) != parserMatcherLocatorFile {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
			if fn.Name != nil {
				parserFuncs[fn.Name.Name] = fn
			}
		})
	}
	return parserFuncs
}

// scanMatcherSymmetry walks all match*Path FuncDecls in parserMatcherLocatorFile
// and fills results with per-matcher parts[0] coverage.
func scanMatcherSymmetry(p *Pass, parserFuncs map[string]*ast.FuncDecl, results map[string]*matcherResult) {
	for _, f := range p.Files {
		if p.Rel(f) != parserMatcherLocatorFile {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
			recordMatcherCoverage(p, fn, parserFuncs, results)
		})
	}
}

// recordMatcherCoverage records parts[0] root/examples coverage for a single
// match*Path FuncDecl into results. Non-matcher FuncDecls (or matchers absent
// from matcherRootSegment) are ignored.
func recordMatcherCoverage(p *Pass, fn *ast.FuncDecl, parserFuncs map[string]*ast.FuncDecl, results map[string]*matcherResult) {
	if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
		return
	}
	funcName := fn.Name.Name
	if _, ok := matcherRootSegment[funcName]; !ok {
		return
	}
	if results[funcName] == nil {
		results[funcName] = &matcherResult{}
	}
	results[funcName].line = p.Fset.Position(fn.Pos()).Line

	allVals := collectMatcherParts0Values(p, fn, parserFuncs)
	if allVals["examples"] {
		results[funcName].foundExamples = true
	}
	if allVals[matcherRootSegment[funcName]] {
		results[funcName].foundRoot = true
	}
}

// collectMatcherParts0Values gathers the set of const-resolved parts[0]
// comparison values from a matcher body plus its direct same-file callees.
func collectMatcherParts0Values(p *Pass, fn *ast.FuncDecl, parserFuncs map[string]*ast.FuncDecl) map[string]bool {
	allVals := scanParts0Values(p, fn.Body)
	for _, callee := range collectCalleeNames(fn.Body) {
		helperFn, ok := parserFuncs[callee]
		if !ok {
			continue
		}
		for v := range scanParts0Values(p, helperFn.Body) {
			allVals[v] = true
		}
	}
	return allVals
}

// scanParts0Values returns a set of const-resolved string values appearing as
// RHS of `parts[0] == <val>` BinaryExpr nodes within root.
func scanParts0Values(p *Pass, root ast.Node) map[string]bool {
	vals := make(map[string]bool)
	scanTopLevelCellRootCalls(root, vals)
	scanParts0Comparisons(p, root, vals)
	return vals
}

func scanTopLevelCellRootCalls(root ast.Node, vals map[string]bool) {
	EachInSubtree[ast.CallExpr](root, func(ce *ast.CallExpr) {
		callee, ok := ce.Fun.(*ast.Ident)
		if !ok || callee.Name != "isTopLevelCellRoot" || len(ce.Args) != 1 {
			return
		}
		if !isParts0Expr(ce.Args[0]) {
			return
		}
		vals["cells"] = true
		vals["corecells"] = true
	})
}

func scanParts0Comparisons(p *Pass, root ast.Node, vals map[string]bool) {
	EachInSubtree[ast.BinaryExpr](root, func(be *ast.BinaryExpr) {
		if be.Op != token.EQL {
			return
		}
		if !isParts0Expr(be.X) {
			return
		}
		val, ok := EvaluateConstString(p.TypesInfo, be.Y)
		if !ok {
			return
		}
		vals[val] = true
	})
}

func isParts0Expr(expr ast.Expr) bool {
	idx, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}
	ident, ok := idx.X.(*ast.Ident)
	if !ok || ident.Name != "parts" {
		return false
	}
	idxLit, ok := idx.Index.(*ast.BasicLit)
	return ok && idxLit.Kind == token.INT && idxLit.Value == "0"
}

// collectCalleeNames returns the names of bare-ident function calls in root.
func collectCalleeNames(root ast.Node) []string {
	var names []string
	EachInSubtree[ast.CallExpr](root, func(ce *ast.CallExpr) {
		id, ok := ce.Fun.(*ast.Ident)
		if !ok {
			return
		}
		names = append(names, id.Name)
	})
	return names
}

// collectSymmetryDiagnostics converts the results map to diagnostics, each
// anchored to parserMatcherLocatorFile (the file all matchers live in). A
// missing matcher anchors to the file head; a matcher missing a parts[0]
// comparison anchors to its declaration line.
func collectSymmetryDiagnostics(results map[string]*matcherResult) []Diagnostic {
	var diags []Diagnostic
	for funcName, wantRoot := range matcherRootSegment {
		r, found := results[funcName]
		if !found {
			diags = append(diags, diagFile(parserMatcherLocatorFile,
				"PARSER-MATCHER-EXAMPLES-SYMMETRY-01: matcher "+funcName+
					" not found in kernel/metadata/locator_conventional.go"))
			continue
		}
		if !r.foundRoot {
			diags = append(diags, diagAt(parserMatcherLocatorFile, r.line,
				"PARSER-MATCHER-EXAMPLES-SYMMETRY-01: matcher "+funcName+
					` is missing parts[0] == "`+wantRoot+`" comparison (root segment) in its body or direct callees`))
		}
		if !r.foundExamples {
			diags = append(diags, diagAt(parserMatcherLocatorFile, r.line,
				"PARSER-MATCHER-EXAMPLES-SYMMETRY-01: matcher "+funcName+
					` is missing parts[0] == "examples" comparison (examples support) in its body or direct callees`))
		}
	}
	return diags
}

// runUpstreamCoverageCheck returns diagnostics for any match*Path function
// discovered in parserMatcherLocatorFile but absent from matcherRootSegment.
// This closes the gap where a newly added matcher silently bypasses the
// primary check.
func runUpstreamCoverageCheck(t *testing.T) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{"./kernel/metadata/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != parserMatcherMetadataPkg {
			return nil
		}
		for _, f := range p.Files {
			if p.Rel(f) != parserMatcherLocatorFile {
				continue
			}
			rel := p.Rel(f)
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				diags = append(diags, uncoveredMatcherDiag(rel, p.Fset.Position(fn.Pos()).Line, fn)...)
			})
		}
		return nil
	})
	return diags
}

// uncoveredMatcherDiag returns a diagnostic when fn is a match*Path matcher
// absent from matcherRootSegment (a newly added matcher silently bypassing the
// primary check), else nil. rel/line anchor the diagnostic to the matcher's
// declaration so Report renders a clickable prefix.
func uncoveredMatcherDiag(rel string, line int, fn *ast.FuncDecl) []Diagnostic {
	if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
		return nil
	}
	if _, ok := matcherRootSegment[fn.Name.Name]; ok {
		return nil
	}
	return []Diagnostic{diagAt(rel, line,
		"PARSER-MATCHER-EXAMPLES-SYMMETRY-01: matcher "+fn.Name.Name+
			" found in parser.go but not in matcherRootSegment — "+
			"add expected root segment to the test")}
}
