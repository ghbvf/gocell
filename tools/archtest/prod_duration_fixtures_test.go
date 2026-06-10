//go:build archtest

// INVARIANT: PROD-DURATION-CONST-01
//
// prod_duration_fixtures_test.go — fixture-based regression tests for the
// PROD-DURATION-CONST-01 invariant. Each subpackage under testdata/prod_duration_fixtures/
// exercises one bypass path or boundary condition.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runFixtureScan loads the fixture package at fixtureDir and returns the sorted
// slice of "file.go:line" violation strings using the same walk+predicates as
// TestProdDurationConst. Paths outside the fixture module root (stdlib, deps)
// are excluded via fileroles.Rel returning ok=false.
func runFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	var violations []string
	Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)

				raw := scanProdDurationAST(p.Fset, f, rel, p.TypesInfo)
				violations = append(violations, raw...)
			}
			return nil
		})

	return violations
}

// parseDurationDiags converts the "rel:line: message" strings produced by
// scanProdDurationAST into []Diagnostic for golden-file comparison.
// Lines that do not match the expected prefix format are silently skipped
// (they should never appear in well-formed fixture output).
func parseDurationDiags(raw []string) []Diagnostic {
	var out []Diagnostic
	for _, s := range raw {
		// Format: "rel:line: message"
		// rel itself may contain path separators but not colons on the target OS.
		firstColon := strings.Index(s, ":")
		if firstColon < 0 {
			continue
		}
		rel := s[:firstColon]
		rest := s[firstColon+1:]
		secondColon := strings.Index(rest, ":")
		if secondColon < 0 {
			continue
		}
		lineStr := rest[:secondColon]
		line, err := strconv.Atoi(lineStr)
		if err != nil {
			continue
		}
		msg := strings.TrimPrefix(rest[secondColon+1:], " ")
		out = append(out, Diagnostic{Rel: rel, Line: line, Message: msg})
	}
	return out
}

// TestProdDurationConstFixtures runs the PROD-DURATION-CONST-01 scanner over
// all fixture subpackages and compares against diag.golden.
// GREEN fixtures have an empty golden; RED fixtures capture the real output.
func TestProdDurationConstFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := filepath.Join(root, "tools", "archtest", "testdata", "prod_duration_fixtures")

	dirs := []string{
		// GREEN — must produce 0 violations.
		"package_const_passes",
		"package_const_block_passes",
		"zero_literal_passes",
		"non_duration_literal_passes",
		"time_now_add_named_passes",

		// RED — must produce violations.
		"func_local_const_violates",
		"alias_import_violates",
		"dot_import_violates",
		"non_whitelist_sink_violates",
		"composite_field_violates",
		"return_violates",
		"var_init_violates",
		"var_basicLit_violates",
		"time_now_add_literal_violates",
		"switch_case_violates",
		"for_init_violates",
		"closure_violates",
		"type_conversion_violates",
		"chained_unit_violates",
		"time_duration_cast_violates",
		"negative_literal_violates",
		"addition_violates",
		"build_tag_e2e_violates",
		"build_tag_integration_violates",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(fixturesBase, dir)
			raw := runFixtureScan(t, fixtureDir)
			diags := parseDurationDiags(raw)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}

// TestProdDurationConstFailsClosedOnLoadError is intentionally removed: the
// fail-closed property is now enforced by Run(t, StandaloneModule(...)) itself (it calls
// t.Fatalf on load errors), making a separate test redundant.
