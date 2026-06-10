//go:build archtest

// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01
//
// Every match{Kind}Path function in kernel/metadata/locator_conventional.go must accept both
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
// AI-robust evaluation: Medium. Two layers:
//
//  1. Primary symmetry check (downstream): typed AST + EvaluateConstString
//     (resolves untyped const, cross-package Ident, const string concat). Not
//     a string anchor; robust to whitespace/identifier-rename refactors but
//     fails immediately if a matcher omits examples/ support.
//
//  2. Upstream coverage check: after the main test loop,
//     TestParserMatcherExamplesSymmetry01 iterates all FuncDecls in parser.go
//     matching ^match.+YAML$ and asserts each name appears as a key in
//     matcherRootSegment. This closes the gap where a newly added match*YAML
//     function could silently bypass the primary check by simply being absent
//     from the hand-maintained map. Mechanism = typed FuncDecl discovery via
//     regex cross-check against the map; Medium because it is an archtest
//     (not compile-time) but the discovery is type-aware rather than a string
//     anchor.
//
// Cannot be Hard: the matchers must be Go funcs (parser uses path string +
// boolean return); a codegen funnel for matchers would be possible but is a
// disproportionate cost for 5 callsites. Hard upgrade tracked under backlog
// `PARSER-MATCHER-SYMMETRY-HARDEN` (codegen-based matcher table; tracked
// under issue #868).
//
// Blind-spot self-check (as required by ai-robust.md §"工具选定后强制盲区自检"):
//
// EvaluateConstString evaluates BasicLit / Ident / SelectorExpr / BinaryExpr
// via go/types constant folding. AST forms OUTSIDE its declared scope:
//
//  1. Index expressions NOT of the form `parts[<intlit>]` — e.g. `parts[i]`
//     with a variable index. Such comparisons are NOT visited by this archtest
//     (the LHS shape guard rejects non-literal indices). Blind spot: a matcher
//     could use a variable-index parts comparison and evade the check.
//     Reverse self-test: TestParserMatcherSymmetry_BlindSpot_VariableIndex asserts
//     that variable-index comparisons do NOT appear in production matchers or
//     their direct callees in parser.go.
//
//  2. Switch statements with `parts[N]` as the switch tag. GoCell matchers
//     currently use if/else chains (BinaryExpr). A future switch refactor would
//     evade this check. Reverse self-test: TestParserMatcherSymmetry_BlindSpot_SwitchStmt
//     asserts that no match*YAML function (or its direct callees) uses a
//     SwitchStmt whose tag is `parts[<intlit>]`.
//
//  3. Helpers more than one call-level deep from the matcher wrapper. The archtest
//     only walks direct callees (depth 1). This is sufficient for the current code
//     structure (all helpers are called directly) and acceptable given the depth
//     constraint is an explicit design decision.
//
// Blind-spot probes share scope with main check (matcher + direct callees) via
// the matcherAndCalleeBodies helper.
package archtest

import (
	"go/ast"
	"go/token"
	"testing"
)

// TestParserMatcherExamplesSymmetry01 dogfoods CheckParserMatcherExamplesSymmetry01
// — the single rule body — so the exact scan an external cell would import is
// the one GoCell enforces (no parallel inline rule body).
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01
// AI-robust: Medium (typed AST + EvaluateConstString); see file-level godoc for rationale.
func TestParserMatcherExamplesSymmetry01(t *testing.T) {
	t.Parallel()
	Report(t, "PARSER-MATCHER-EXAMPLES-SYMMETRY-01", CheckParserMatcherExamplesSymmetry01(t, ConfigForExternalCell{}))
}

// TestParserMatcherSymmetry_BlindSpot_VariableIndex is the reverse self-test
// asserting that no match*YAML function body (or its direct callees in parser.go)
// uses a variable-index parts comparison (parts[i] where i is not an integer literal)
// in a position that could evade the main check's parts[0] root-segment extraction.
// Such a form would be outside EvaluateConstString's scope and would evade the main test.
//
// Scope: matcher body + direct callees in parser.go (matches main check scope via
// matcherAndCalleeBodies helper).
//
// Exemption: `parts[len(parts)-k]` (index is a BinaryExpr whose LHS is a CallExpr)
// is the canonical "last element" pattern — it is categorically not an index-0 access
// and therefore cannot evade the root-segment check. Such indices are intentionally
// excluded from this probe.
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot reverse probe)
// Reverse probe: asserts variable-index parts comparisons absent in production matchers
// and their direct callees, except for len(parts)-based tail-element accesses.
func TestParserMatcherSymmetry_BlindSpot_VariableIndex(t *testing.T) {
	t.Parallel()

	_ = Run(t, Typed(TypedOpts{}, []string{"./kernel/metadata/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != parserMatcherMetadataPkg {
			return nil
		}
		for _, f := range p.Files {
			if p.Rel(f) != parserMatcherLocatorFile {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
					return
				}
				for _, body := range matcherAndCalleeBodies(f, fn) {
					EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
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
						if _, ok := idx.Index.(*ast.BasicLit); ok {
							return
						}

						if binIdx, ok := idx.Index.(*ast.BinaryExpr); ok {
							if _, lhsIsCall := binIdx.X.(*ast.CallExpr); lhsIsCall {
								return
							}
						}
						pos := p.Fset.Position(be.Pos())
						t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot): "+
							"matcher %s (or its callee) uses variable-index parts comparison at %s; "+
							"EvaluateConstString cannot resolve this — update the main test if intentional",
							fn.Name.Name, pos)
					})
				}
			})
		}
		return nil
	})
}

// TestParserMatcherSymmetry_BlindSpot_SwitchStmt is the reverse self-test
// asserting that no match*YAML function body (or its direct callees in parser.go)
// uses a SwitchStmt whose tag is `parts[N]` (where N is an integer literal).
// That specific form would evade the BinaryExpr walk in the main test because the
// parts-index comparison appears as a switch tag rather than as an == BinaryExpr.
//
// Scope: matcher body + direct callees in parser.go (matches main check scope via
// matcherAndCalleeBodies helper).
//
// Probe is precisely scoped to `switch parts[<intlit>] { ... }` — a switch tag
// that is an IndexExpr of the identifier "parts" with an integer-literal index.
// Unrelated switch forms (e.g. type switches, switches on other expressions) are
// intentionally NOT flagged; they do not evade the BinaryExpr walk.
//
// INVARIANT: PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot reverse probe)
// Reverse probe: asserts switch parts[N] form absent in production match*YAML
// function bodies and their direct callees.
func TestParserMatcherSymmetry_BlindSpot_SwitchStmt(t *testing.T) {
	t.Parallel()

	_ = Run(t, Typed(TypedOpts{}, []string{"./kernel/metadata/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != parserMatcherMetadataPkg {
			return nil
		}
		for _, f := range p.Files {
			if p.Rel(f) != parserMatcherLocatorFile {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || !matchYAMLFuncRE.MatchString(fn.Name.Name) {
					return
				}
				for _, body := range matcherAndCalleeBodies(f, fn) {
					EachInSubtree[ast.SwitchStmt](body, func(sw *ast.SwitchStmt) {
						if sw.Tag == nil {
							return
						}
						idx, ok := sw.Tag.(*ast.IndexExpr)
						if !ok {
							return
						}
						ident, ok := idx.X.(*ast.Ident)
						if !ok || ident.Name != "parts" {
							return
						}
						idxLit, ok := idx.Index.(*ast.BasicLit)
						if !ok || idxLit.Kind != token.INT {
							return
						}
						pos := p.Fset.Position(sw.Pos())
						t.Errorf("INVARIANT PARSER-MATCHER-EXAMPLES-SYMMETRY-01 (blind-spot): "+
							"matcher %s (or its callee) uses switch parts[%s] at %s; the BinaryExpr "+
							"walk in the main test does not cover switch cases — update the archtest "+
							"if a switch refactor is intentional",
							fn.Name.Name, idxLit.Value, pos)
					})
				}
			})
		}
		return nil
	})
}
