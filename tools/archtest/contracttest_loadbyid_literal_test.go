//go:build archtest

// INVARIANT: CONTRACTTEST-LOADBYID-LITERAL-01
//
// CONTRACTTEST-LOADBYID-LITERAL-01 — every call to contracttest.LoadByID
// must supply a compile-time constant string as the third argument (the
// contract ID). Runtime-computed contract IDs are forbidden because they
// prevent static analysis tools (including CONTRACT-PATH-QUERY-COVERAGE-01)
// from associating a LoadByID call site with a specific contract.
//
// Tool: Run(t, Production(TypedOpts{Tests: true})) (040 Pass-Driver) — resolves the
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
// Declared blind spots (ai-robust.md §"工具选定后强制盲区自检"):
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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContracttestLoadByIDLiteral01 dogfoods CheckContracttestLoadByIDLiteral01
// — the single rule body — so the exact scan an external cell would import is
// the one GoCell enforces (no parallel inline rule body).
func TestContracttestLoadByIDLiteral01(t *testing.T) {
	t.Parallel()
	Report(t, "CONTRACTTEST-LOADBYID-LITERAL-01", CheckContracttestLoadByIDLiteral01(t, ConfigForExternalCell{}))
}

// TestContracttestLoadByIDLiteral01_RedComputedID is the cross-package reverse
// self-check: a fixture file (build tag archtest_fixture) calls
// contracttest.LoadByID with a runtime-computed (function-call) ID. The rule
// MUST report it as a violation.
func TestContracttestLoadByIDLiteral01_RedComputedID(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/contracttest_loadbyid_literal_fixtures/red_computed_id/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			return scanLoadByIDLiteralViolations(p)
		})

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
	diags := Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			return scanLoadByIDLiteralViolations(p)
		})

	require.NotEmpty(t, diags,
		"CONTRACTTEST-LOADBYID-LITERAL-01 reverse self-check: fixture must produce ≥1 violation "+
			"(fixture calls same-package LoadByID with tt.contractID struct field access)")
}
