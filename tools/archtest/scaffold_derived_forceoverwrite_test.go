// INVARIANT: SCAFFOLD-DERIVED-FORCEOVERWRITE-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckScaffoldDerivedForceOverwrite + collectDerivedOverwriteViolations
// + the type-aware resolution helpers + the full package godoc) lives in the
// non-test companion scaffold_derived_forceoverwrite.go, so external Cell repos
// can import and run it via StandardCellRules / RunStandardCellRules (M3 #1302).
package archtest

import (
	"path/filepath"
	"testing"
)

// TestScaffoldDerivedForceOverwrite dogfoods SCAFFOLD-DERIVED-FORCEOVERWRITE-01
// against GoCell itself by calling the same CheckScaffoldDerivedForceOverwrite
// that StandardCellRules (and external Cell repos via RunStandardCellRules) use
// — single source, no parallel rule body.
func TestScaffoldDerivedForceOverwrite(t *testing.T) {
	t.Parallel()
	Report(t, ruleScaffoldDerivedForceOverwrite01,
		CheckScaffoldDerivedForceOverwrite(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestScaffoldDerivedForceOverwrite_FixtureGate is the RED-fixture precision
// gate proving behavior-equivalence of the migrated scanner: it runs the same
// collectDerivedOverwriteViolations that the production Check uses against a set
// of synthetic fixture packages, each owning a diag.golden capturing the exact
// expected output (Rel:Line: Message). GREEN fixtures have an empty golden.
//
//   - red_outside_ctor: a direct DerivedOverwrite call outside
//     planDerivedArtifact (forward caller-allowlist violation, SelectorExpr form).
//   - red_dot_import_call: a dot-imported direct call (forward violation, bare
//     *ast.Ident form — covers callsDerivedOverwrite's Ident branch).
//   - red_indirect_ref: taking the DerivedOverwrite function value (reverse
//     indirect-reference blind-spot violation).
//   - green_compliant: imports pathsafe and uses the sanctioned NewPlanSet
//     constructor but never references DerivedOverwrite (0 violations).
func TestScaffoldDerivedForceOverwrite_FixtureGate(t *testing.T) {
	t.Parallel()
	dirs := []string{
		"red_outside_ctor",
		"red_dot_import_call",
		"red_indirect_ref",
		"green_compliant",
	}
	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixturePattern := "./tools/archtest/testdata/scaffold_derived_fixtures/" + dir
			var diags []Diagnostic
			// Load using module root so the fixture's import of pkg/pathsafe resolves.
			_ = Run(t, Typed(TypedOpts{}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
				diags = append(diags, collectDerivedOverwriteViolations(p)...)
				return nil
			})
			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"scaffold_derived_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}
