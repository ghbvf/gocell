// INVARIANT: CONTRACTTEST-LOADBYID-LITERAL-01
//
// CONTRACTTEST-LOADBYID-LITERAL-01 — every call to contracttest.LoadByID
// must supply a compile-time constant string as the third argument (the
// contract ID). Runtime-computed contract IDs are forbidden because they
// prevent static analysis tools (including CONTRACT-PATH-QUERY-COVERAGE-01)
// from associating a LoadByID call site with a specific contract.
//
// Tool: RunTypedProduction (040 Pass-Driver) with Tests=true — resolves the
// callee via *types.Info.Uses against tests/contracttest.LoadByID for both
// the cross-package selector form (contracttest.LoadByID) and the same-package
// bare-ident form (LoadByID, used inside tests/contracttest's own test files).
// The third argument is then checked via typeseval.EvaluateConstString, which
// accepts BasicLit / const-bound Ident / SelectorExpr-to-const /
// BinaryExpr-of-consts via go/types constant folding. Runtime forms (struct
// field access, function call, plain variable assignment) are rejected.
//
// Mirroring MESSAGE-CONST-LITERAL-01's enforcement shape keeps the const-
// literal funnel single-source across the archtest suite.
// NOT registered in internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. A call to a local wrapper function that in turn calls LoadByID with a
//     constant: func load(t, root, id) { contracttest.LoadByID(t, root, id) }.
//     The wrapper call site has a non-constant argument (a parameter) and
//     escapes this rule; the inner LoadByID has a non-constant argument and
//     would be caught. Compensation: rule catches the inner violation;
//     production code should not introduce wrapper helpers that defer
//     constant resolution.
//
// Reverse self-checks:
//
//   - TestContracttestLoadByIDLiteral01_RedComputedID — fixture file (build tag
//     archtest_fixture) calls cross-package contracttest.LoadByID with a
//     computed (function-call) ID; the rule MUST flag it.
//   - TestContracttestLoadByIDLiteral01_RedStructFieldID — fixture file calls
//     same-package LoadByID inside a table-driven test, passing a struct field
//     access (tt.contractID) as the ID. The rule MUST flag it — proves both
//     the Ident-form callee resolution and the EvaluateConstString rejection
//     of non-const arguments.
package archtest

import (
	"go/ast"
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
// contracttest.LoadByID in the production+test codebase supplies a compile-time
// constant string as the third argument (the contract ID). Non-constant
// arguments (struct field access, function calls, variables) are forbidden.
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

// TestContracttestLoadByIDLiteral01_RedComputedID is the cross-package reverse
// self-check: a fixture file (build tag archtest_fixture) calls
// contracttest.LoadByID with a runtime-computed (function-call) ID. The rule
// MUST report it as a violation.
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
			"(fixture calls contracttest.LoadByID with a runtime-computed contract ID)")
}

// TestContracttestLoadByIDLiteral01_RedStructFieldID is the same-package
// reverse self-check: a fixture file calls same-package LoadByID inside a
// table-driven test with a struct field access as the ID. Catches two
// regressions in one fixture:
//   - the *ast.Ident callee branch of isLoadByIDCall (same-package bare call
//     form) — without that branch, the call site escapes detection;
//   - the EvaluateConstString rejection of runtime field access — without it,
//     a *ast.BasicLit-only check would let the struct field through.
func TestContracttestLoadByIDLiteral01_RedStructFieldID(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/contracttest_loadbyid_literal_fixtures/red_struct_field_id/..."
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
			"(fixture calls same-package LoadByID with tt.contractID struct field access)")
}

// scanLoadByIDLiteralViolations scans p.Files for contracttest.LoadByID calls
// whose third argument does not resolve to a compile-time constant string,
// and returns diagnostics for each violation.
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
			// Third argument (index 2) must resolve to a compile-time const
			// string via go/types constant folding (covers BasicLit, const
			// Ident, SelectorExpr to const, BinaryExpr of consts). Runtime
			// forms (struct field access, function call, plain variable)
			// fail EvaluateConstString and produce a diagnostic.
			if _, ok := EvaluateConstString(p.TypesInfo, call.Args[2]); ok {
				return // compliant
			}
			pos := p.Fset.Position(call.Args[2].Pos())
			diags = append(diags, Diagnostic{
				Rel:  p.Rel(file),
				Line: pos.Line,
				Message: "CONTRACTTEST-LOADBYID-LITERAL-01: contracttest.LoadByID third argument " +
					"must be a compile-time constant string; got runtime expression",
			})
		})
	}
	return diags
}

// isLoadByIDCall reports whether funExpr resolves (via *types.Info) to
// contracttest.LoadByID. Accepts both forms:
//   - cross-package selector: contracttest.LoadByID(...)
//   - same-package bare identifier: LoadByID(...) inside the contracttest
//     package's own test files.
//
// Same-package detection (the *ast.Ident branch) is required because the
// contracttest package owns several internal table-driven tests that bare-call
// LoadByID; without this branch they escape the rule entirely.
func isLoadByIDCall(funExpr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch fn := funExpr.(type) {
	case *ast.SelectorExpr:
		if fn.Sel == nil || fn.Sel.Name != contracttestLoadByIDFunc {
			return false
		}
		ident = fn.Sel
	case *ast.Ident:
		if fn.Name != contracttestLoadByIDFunc {
			return false
		}
		ident = fn
	default:
		return false
	}
	obj := info.Uses[ident]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == contracttestLoadByIDPkg && fn.Name() == contracttestLoadByIDFunc
}
