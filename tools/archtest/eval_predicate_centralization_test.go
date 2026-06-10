//go:build archtest

// INVARIANT: TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckEvalPredicateCentralization01 + scanFileForEvalPredicateViolations
// + isConstraintExprEvalCall + isBuildContextPredicateCallee +
// isAllFalseSentinelFuncLit + formatEvalCallee + evalPredicateViolation + the
// full package godoc, all module-path-agnostic via PlatformModulePath) lives in
// the non-test companion eval_predicate_centralization.go (M3 #1639) so it is
// fork-safe — single source, no parallel rule body.
package archtest

import (
	"path/filepath"
	"testing"
)

// TestEvalPredicateCentralization01 dogfoods
// TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 against GoCell itself by calling the
// same CheckEvalPredicateCentralization01 that holds the importable rule body —
// single source. Every constraint.Expr.Eval callsite in
// tools/archtest/*_test.go must pass either typeseval.BuildContextPredicate(...)
// (Form A) or an inline `func(_ string) bool { return false }` sentinel (Form B).
func TestEvalPredicateCentralization01(t *testing.T) {
	Report(t, evalPredicateRuleID, CheckEvalPredicateCentralization01(t, ConfigForExternalCell{}))
}

// TestEvalPredicateCentralizationFixtures is the "test the test" meta-test
// for TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 detection helpers. Each fixture
// package under
//
//	tools/archtest/testdata/eval_predicate_centralization_fixtures/<case>/
//
// is a synthetic single-file Go package that demonstrates one canonical
// shape (GREEN, 0 violations expected) or one violation shape (RED, the
// expected violation line(s) listed in the table below). Loading goes
// through Run(t, Typed(...)) with full types.Info so the type-aware
// detection paths (isConstraintExprEvalCall via ResolveMethodCall;
// isBuildContextPredicateCallee via ResolvePackageRef) are exercised the
// same way as the main test.
//
// Adding a new fixture: drop a `<case>/usage.go` file with the demonstration
// shape and add a row to `cases` below. testdata/ is skipped by the Go
// toolchain so fixtures never run in default builds.
func TestEvalPredicateCentralizationFixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	fixtureBase := filepath.Join(root, "tools", "archtest", "testdata",
		"eval_predicate_centralization_fixtures")

	// Each fixture dir owns a diag.golden capturing the rule's real output
	// (Rel:Line: Message); GREEN fixtures have an empty golden. Expected line
	// numbers live in the regenerated golden, never in this table. See ADR
	// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
	dirs := []string{
		// GREEN — Form A and Form B canonical shapes accepted.
		"form_a_good", "form_b_good",
		// RED — hand-rolled predicate (most common drift form).
		"inline_predicate_red",
		// RED — var binding indirection (arg is Ident, not CallExpr / FuncLit).
		"var_binding_red",
		// RED — FuncLit body has multi-statement, fails sentinel single-stmt check.
		"funclit_multi_stmt_red",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixtureDir := filepath.Join(fixtureBase, dir)
			var diags []Diagnostic
			Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
				func(p *Pass) []Diagnostic {
					for _, f := range p.Files {
						rel := p.Rel(f)
						for _, v := range scanFileForEvalPredicateViolations(p.Fset, f, p.TypesInfo, rel) {
							diags = append(diags, Diagnostic{Rel: v.Rel, Line: v.Line, Message: v.Form})
						}
					}
					return nil
				})

			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
