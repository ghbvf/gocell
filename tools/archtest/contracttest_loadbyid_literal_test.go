// INVARIANT: CONTRACTTEST-LOADBYID-LITERAL-01
//
// CONTRACTTEST-LOADBYID-LITERAL-01 — every call to contracttest.LoadByID
// must supply a string literal as the third argument (the contract ID). Dynamic
// or computed contract IDs are forbidden because they prevent static analysis
// tools (including CONTRACT-PATH-QUERY-COVERAGE-01) from associating a
// LoadByID call site with a specific contract.
//
// Tool: RunTypedProduction (040 Pass-Driver) with Tests=true — uses
// *types.Info.Uses to resolve whether the callee is
// tests/contracttest.LoadByID, then asserts the third argument is
// *ast.BasicLit with Kind==token.STRING. This prevents bypass via import
// alias renaming. NOT registered in internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. A variable initialized from a string literal and then passed as the
//     third argument: const id = "http.foo.v1"; LoadByID(t, root, id).
//     The argument is an Ident, not a BasicLit — this is flagged by this rule
//     even though the effective value is static. Compensation: this is the
//     intended behavior; only inline literals are accepted so that the
//     contract ID is unambiguous at the call site. Production code should
//     use inline literals directly.
//
//  2. A call to a local wrapper function that in turn calls LoadByID with a
//     literal: func load(t, root, id) { contracttest.LoadByID(t, root, id) }.
//     The wrapper call site has a non-literal argument and escapes this rule;
//     the inner LoadByID has a non-literal argument and would be caught.
//     Compensation: rule catches the inner violation; production code should
//     not introduce wrapper helpers that defer literal resolution.
//
// Reverse self-check: TestContracttestLoadByIDLiteral01_RedComputedID
// loads a fixture package (archtest_fixture build tag) with a computed
// contract ID argument; the rule MUST flag it as a violation.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

// contracttestLoadByIDPkg is the import path of the contracttest package
// whose LoadByID function is under scrutiny.
const contracttestLoadByIDPkg = "github.com/ghbvf/gocell/tests/contracttest"

// contracttestLoadByIDFunc is the function name locked by this rule.
const contracttestLoadByIDFunc = "LoadByID"

// TestContracttestLoadByIDLiteral01 asserts that every call to
// contracttest.LoadByID in the production+test codebase supplies a string
// literal as the third argument (the contract ID). Non-literal arguments
// (identifiers, function calls, concatenations) are forbidden.
func TestContracttestLoadByIDLiteral01(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{Tests: true}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		return scanLoadByIDLiteralViolations(p)
	})

	Report(t, "CONTRACTTEST-LOADBYID-LITERAL-01", diags)
}

// TestContracttestLoadByIDLiteral01_RedComputedID is the reverse self-check:
// a fixture file (build tag archtest_fixture) calls LoadByID with a computed
// (non-literal) ID. The rule MUST report it as a violation.
func TestContracttestLoadByIDLiteral01_RedComputedID(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/contracttest_loadbyid_literal_fixtures/red_computed_id/..."
	diags := RunTyped(t, TypedOpts{Tests: true, Tags: []string{"archtest_fixture"}},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			return scanLoadByIDLiteralViolations(p)
		},
	)

	require.NotEmpty(t, diags,
		"CONTRACTTEST-LOADBYID-LITERAL-01 reverse self-check: fixture must produce ≥1 violation "+
			"(fixture calls LoadByID with a computed non-literal contract ID)")
}

// scanLoadByIDLiteralViolations scans p.Files for contracttest.LoadByID calls
// whose third argument is not a string literal, and returns diagnostics for
// each violation.
func scanLoadByIDLiteralViolations(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if len(call.Args) < 3 {
				return
			}
			if !isLoadByIDCall(call.Fun, p.TypesInfo) {
				return
			}
			// Third argument (index 2) must be a string literal.
			lit, ok := call.Args[2].(*ast.BasicLit)
			if ok && lit.Kind == token.STRING {
				return // compliant
			}
			pos := p.Fset.Position(call.Args[2].Pos())
			diags = append(diags, Diagnostic{
				Rel:     p.Rel(file),
				Line:    pos.Line,
				Message: "CONTRACTTEST-LOADBYID-LITERAL-01: contracttest.LoadByID third argument must be a string literal; got non-literal expression",
			})
		})
	}
	return diags
}

// isLoadByIDCall reports whether funExpr resolves (via *types.Info) to
// contracttest.LoadByID.
func isLoadByIDCall(funExpr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != contracttestLoadByIDFunc {
		return false
	}
	obj := info.Uses[sel.Sel]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == contracttestLoadByIDPkg && fn.Name() == contracttestLoadByIDFunc
}
