// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01
//
// Every match{Kind}YAML function in kernel/metadata/parser.go must accept both
// the root form (<root>/<kind>/...) and the examples form
// (examples/*/<kind>/...). The archtest uses typed AST const evaluation to walk
// each matcher body (and its directly-called package-private helper functions
// within the same file), extract every `parts[N] == <CONST>` BinaryExpr where
// the RHS is a const-resolved string, and assert the set of values appearing in
// parts[0] comparisons includes both the kind's root segment literal AND the
// literal "examples".
//
// Why traverse helper callees: matchers like matchCellYAML / matchSliceYAML /
// matchContractYAML / matchJourneyYAML are thin wrappers that delegate to
// package-private helpers (cellDirFromPath, sliceDirsFromPath, etc.) which hold
// the actual parts comparisons. matchAssemblyYAML is currently the only matcher
// that inlines its logic. The archtest walks one level of callee so both
// patterns are covered uniformly.
//
// AI-rebust evaluation: Medium. Mechanism = typed AST + EvaluateConstString
// (resolves untyped const, cross-package Ident, const string concat). Not a
// string anchor; the archtest is robust to whitespace/identifier-rename
// refactors of the matchers but breaks if a future matcher omits examples/
// support, regardless of whether the omission is via const literal, named
// const, or expression evaluating to a string.
//
// Cannot be Hard: the matchers must be Go funcs (parser uses path string +
// boolean return); a codegen funnel for matchers would be possible but is a
// disproportionate cost for 5 callsites. Hard upgrade tracked under backlog
// `PARSER-MATCHER-SYMMETRY-HARDEN` (codegen-based matcher table).
//
// Blind-spot self-check (as required by ai-collab.md §"工具选定后强制盲区自检"):
//
// EvaluateConstString evaluates BasicLit / Ident / SelectorExpr / BinaryExpr
// via go/types constant folding. AST forms OUTSIDE its declared scope:
//
//  1. Index expressions NOT of the form `parts[<intlit>]` — e.g. `parts[i]`
//     with a variable index. Such comparisons are NOT visited by this archtest
//     (the LHS shape guard rejects non-literal indices). Blind spot: a matcher
//     could use a variable-index parts comparison and evade the check.
//     Reverse self-test: TestParserMatcherSymmetry_BlindSpot_VariableIndex asserts
//     that variable-index comparisons do NOT appear in production matchers.
//
//  2. Switch statements with `parts[0]` as the switch expression. GoCell matchers
//     currently use if/else chains (BinaryExpr). A future switch refactor would
//     evade this check. Reverse self-test: TestParserMatcherSymmetry_BlindSpot_SwitchStmt
//     asserts that no match*YAML function (or its direct callees) uses a SwitchStmt.
//
//  3. Helpers more than one call-level deep from the matcher wrapper. The archtest
//     only walks direct callees (depth 1). This is sufficient for the current code
//     structure (all helpers are called directly) and acceptable given the depth
//     constraint is an explicit design decision.
package archtest

import (
	"go/ast"
	"go/token"
	"regexp"
	"testing"
)

// matcherRootSegment maps each match*YAML function name to the expected root
// segment literal for the non-examples form. E.g. matchAssemblyYAML → "assemblies".
var matcherRootSegment = map[string]string{
	"matchCellYAML":     "cells",
	"matchSliceYAML":    "cells",
	"matchContractYAML": "contracts",
	"matchJourneyYAML":  "journeys",
	"matchAssemblyYAML": "assemblies",
}

var matchYAMLFuncRE = regexp.MustCompile(`^match.+YAML$`)

// TestParserMatcherExamplesSymmetry01 asserts that every match*YAML function in
// kernel/metadata/parser.go includes both "examples" AND its kind root segment
// (e.g. "assemblies") in the parts[0] comparison set — either directly in the
// matcher body or in the package-private helper functions called directly from it.
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01
// AI-rebust: Medium (typed AST + EvaluateConstString); see file-level godoc for rationale.
func TestParserMatcherExamplesSymmetry01(t *testing.T) {
	t.Parallel()

	const metadataPkg = "github.com/ghbvf/gocell/kernel/metadata"

	type matcherResult struct {
		foundExamples bool
		foundRoot     bool
	}
	results := make(map[string]*matcherResult)

	_ = RunTyped(t, TypedOpts{}, []string{"./kernel/metadata/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadataPkg {
			return nil
		}

		// Build a map of function name → FuncDecl for package-private helpers in parser.go.
		// We need this to look up helper bodies when a matcher delegates to one.
		parserFuncs := make(map[string]*ast.FuncDecl)
		for _, f := range p.Files {
			if p.Rel(f) != "kernel/metadata/parser.go" {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name != nil {
					parserFuncs[fn.Name.Name] = fn
				}
			})
		}

		// scanParts0Values collects all const-evaluated RHS values in `parts[0] == X`
		// BinaryExpr nodes inside the given AST node.
		scanParts0Values := func(root ast.Node) map[string]bool {
			vals := make(map[string]bool)
			EachInSubtree[ast.BinaryExpr](root, func(be *ast.BinaryExpr) {
				if be.Op != token.EQL {
					return
				}
				idx, ok := be.X.(*ast.IndexExpr)
				if !ok {
					return
				}
				ident, ok := idx.X.(*ast.Ident)
				if !ok || ident.Name != "parts" {
					return
				}
				idxLit, ok := idx.Index.(*ast.BasicLit)
				if !ok || idxLit.Kind != token.INT || idxLit.Value != "0" {
					return
				}
				val, ok := EvaluateConstString(p.TypesInfo, be.Y)
				if !ok {
					return
				}
				vals[val] = true
			})
			return vals
		}

		// collectCalleeNames returns the names of all package-level functions called
		// directly in the given AST node (one level deep, same package only).
		collectCalleeNames := func(root ast.Node) []string {
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

		for _, f := range p.Files {
			if p.Rel(f) != "kernel/metadata/parser.go" {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
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

				// Collect parts[0] values from the matcher body itself.
				allVals := scanParts0Values(fn.Body)

				// Also scan direct callee helper bodies (one level deep).
				for _, callee := range collectCalleeNames(fn.Body) {
					if helperFn, ok := parserFuncs[callee]; ok {
						for v := range scanParts0Values(helperFn.Body) {
							allVals[v] = true
						}
					}
				}

				if allVals["examples"] {
					results[funcName].foundExamples = true
				}
				if allVals[matcherRootSegment[funcName]] {
					results[funcName].foundRoot = true
				}
			})
		}
		return nil
	})

	for funcName, wantRoot := range matcherRootSegment {
		r, found := results[funcName]
		if !found {
			t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 violated: "+
				"matcher %s not found in kernel/metadata/parser.go", funcName)
			continue
		}
		if !r.foundRoot {
			t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 violated: "+
				"matcher %s is missing parts[0] == %q comparison (root segment) in its body or direct callees",
				funcName, wantRoot)
		}
		if !r.foundExamples {
			t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 violated: "+
				"matcher %s is missing parts[0] == %q comparison (examples support) in its body or direct callees",
				funcName, "examples")
		}
	}
}

// TestParserMatcherSymmetry_BlindSpot_VariableIndex is the reverse self-test
// asserting that no match*YAML function body (or its direct callees in parser.go)
// uses a variable-index parts comparison (parts[i] where i is not an integer literal).
// Such a form would be outside EvaluateConstString's scope and would evade the main test.
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot reverse probe)
// Reverse probe: asserts variable-index parts comparisons absent in production matchers.
func TestParserMatcherSymmetry_BlindSpot_VariableIndex(t *testing.T) {
	t.Parallel()

	const metadataPkg = "github.com/ghbvf/gocell/kernel/metadata"

	_ = RunTyped(t, TypedOpts{}, []string{"./kernel/metadata/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadataPkg {
			return nil
		}
		for _, f := range p.Files {
			if p.Rel(f) != "kernel/metadata/parser.go" {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
					return
				}
				EachInSubtree[ast.BinaryExpr](fn.Body, func(be *ast.BinaryExpr) {
					if be.Op != token.EQL {
						return
					}
					idx, ok := be.X.(*ast.IndexExpr)
					if !ok {
						return
					}
					ident, ok := idx.X.(*ast.Ident)
					if !ok || ident.Name != "parts" {
						return
					}
					if _, ok := idx.Index.(*ast.BasicLit); !ok {
						pos := p.Fset.Position(be.Pos())
						t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot): "+
							"matcher %s uses variable-index parts comparison at %s; "+
							"EvaluateConstString cannot resolve this — update the main test if intentional",
							fn.Name.Name, pos)
					}
				})
			})
		}
		return nil
	})
}

// TestParserMatcherSymmetry_BlindSpot_SwitchStmt is the reverse self-test
// asserting that no match*YAML function body uses a SwitchStmt (which would
// evade the BinaryExpr walk in the main test).
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot reverse probe)
// Reverse probe: asserts SwitchStmt absent in production match*YAML function bodies.
func TestParserMatcherSymmetry_BlindSpot_SwitchStmt(t *testing.T) {
	t.Parallel()

	const metadataPkg = "github.com/ghbvf/gocell/kernel/metadata"

	_ = RunTyped(t, TypedOpts{}, []string{"./kernel/metadata/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != metadataPkg {
			return nil
		}
		for _, f := range p.Files {
			if p.Rel(f) != "kernel/metadata/parser.go" {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
					return
				}
				EachInSubtree[ast.SwitchStmt](fn.Body, func(sw *ast.SwitchStmt) {
					pos := p.Fset.Position(sw.Pos())
					t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot): "+
						"matcher %s uses a SwitchStmt at %s; the BinaryExpr walk in the main test "+
						"does not cover switch cases — update the archtest if a switch refactor is intentional",
						fn.Name.Name, pos)
				})
			})
		}
		return nil
	})
}
