//go:build archtest

// Package archtest enforces param-level executable contract test coverage.
//
//   - INVARIANT: CONTRACT-PATH-QUERY-COVERAGE-01
//   - INVARIANT: CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01
//
// CONTRACT-PATH-QUERY-COVERAGE-01 — every active HTTP contract that declares
// pathParams or queryParams must have at least one MustRejectPathParam or
// MustRejectQueryParam call site in the corresponding cells/**/contract_test.go
// for EVERY declared param name. Coverage is tracked per
// (contractID, kind, paramName); a contract declaring three query params with
// only one MustRejectQueryParam call site fires two diagnostics (one per
// uncovered param). Previously coverage was a contract-level boolean — review
// F2 fixed.
//
// CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01 — the second argument to every
// Validate{Path,Query}Param / MustReject{Path,Query}Param call must resolve
// to a compile-time constant string via typeseval.EvaluateConstString. Without
// this, a `c.MustRejectQueryParam(t, paramName, "0")` with `paramName` bound
// to a runtime variable would let the COVERAGE-01 attribution mistake the
// abstract variable name for a covered parameter, sneaking past the per-param
// gate. Form mirrors MESSAGE-CONST-LITERAL-01 and CONTRACTTEST-LOADBYID-LITERAL-01.
//
// Tool: Run(t, Production(TypedOpts{...})) (040 Pass-Driver) for test files — uses
// *types.Info.Uses to resolve MustReject{Path,Query}Param / Validate{Path,Query}Param
// receiver calls to the *contracttest.Contract type, and
// typeseval.EvaluateConstString to fold const idents / selectors / binary
// expressions in argument positions. YAML scanning uses
// scanner.EachContentFile (tools/archtest/internal/scanner) to build the
// ground-truth set from contracts/http/**, satisfying
// SCANNER-FRAMEWORK-USAGE-01. NOT registered in
// internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-robust.md §"工具选定后强制盲区自检"):
//
//  1. Contract ID discovered only via LoadByID call sites whose third argument
//     resolves to a compile-time constant string. A call like
//     c := LoadByID(t, root, computedID) with computedID a runtime expression
//     is rejected by CONTRACTTEST-LOADBYID-LITERAL-01 (sibling rule), so the
//     compensation is enforced upstream and we do not need to scan for runtime
//     IDs here.
//
//  2. MustReject{Path,Query}Param called on a *Contract variable loaded in an
//     outer function scope (e.g. passed as argument) rather than via a LoadByID
//     call visible in the same function. The per-variable association tracks
//     LoadByID assignments and MustReject calls within the same function body;
//     cross-function use remains a blind spot. Compensation: contract tests
//     conventionally use a single c per test function; code review.
//
//  3. MustReject{Path,Query}Param call inside a helper function that is not
//     visible to the file-level scan. Compensation: the TypesInfo
//     receiver-type check catches all call sites regardless of nesting depth
//     within the scanned package.
//
// Reverse self-checks:
//
//   - TestContractPathQueryCoverage01_FixtureMissingReject — fixture has
//     pathParams but never calls MustRejectPathParam; the rule MUST report
//     each declared param as uncovered.
//   - TestContractPathQueryCoverage01_FixtureParamPartial — fixture declares
//     two query params (limit, cursor) but only calls
//     MustRejectQueryParam(t, "limit", ...); the rule MUST report the
//     uncovered param (cursor) and MUST NOT report the covered one.
//   - TestContractPathQueryParamNameLiteral01_RedComputedParamName — fixture
//     calls MustRejectQueryParam(t, paramName, ...) with paramName a runtime
//     variable; PARAM-NAME-LITERAL-01 MUST flag it.
//
// AI-robust grade: Medium.
package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContractPathQueryCoverage01 dogfoods CheckContractPathQueryCoverage01
// — the single rule body — so the exact scan an external cell would import is
// the one GoCell enforces (no parallel inline rule body).
func TestContractPathQueryCoverage01(t *testing.T) {
	t.Parallel()
	Report(t, "CONTRACT-PATH-QUERY-COVERAGE-01", CheckContractPathQueryCoverage01(t, ConfigForExternalCell{}))
}

// TestContractPathQueryParamNameLiteral01 dogfoods
// CheckContractPathQueryParamNameLiteral01 — the single rule body — so the
// exact scan an external cell would import is the one GoCell enforces (no
// parallel inline rule body).
func TestContractPathQueryParamNameLiteral01(t *testing.T) {
	t.Parallel()
	Report(t, "CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01", CheckContractPathQueryParamNameLiteral01(t, ConfigForExternalCell{}))
}

// TestContractPathQueryCoverage01_FixtureMissingReject — reverse self-check:
// fixture has pathParams but never calls MustRejectPathParam. With per-param
// granularity the rule MUST report every declared param as uncovered.
func TestContractPathQueryCoverage01_FixtureMissingReject(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixtureContractsRoot := filepath.Join(root,
		"tools", "archtest", "contract_path_query_coverage_fixtures",
		"red_missing_reject", "contracts")

	fixtureContractFile := filepath.Join(fixtureContractsRoot, "http", "test", "paramcoverage", "v1", "contract.yaml")
	if _, err := os.Stat(fixtureContractFile); err != nil {
		t.Fatalf("fixture contract not found at %s: %v", fixtureContractFile, err)
	}

	fixtureRelDir := filepath.Join("tools", "archtest", "contract_path_query_coverage_fixtures",
		"red_missing_reject", "contracts", "http")
	fixtureReqs := buildContractPQRequirementsFromRelDir(t, root, fixtureRelDir)
	require.NotEmpty(t, fixtureReqs, "fixture must have at least one contract with path/queryParams")

	coverage := newPQCoverage()
	fixturePattern := "./tools/archtest/contract_path_query_coverage_fixtures/red_missing_reject/..."
	_ = Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				attributePQCoverageFromFile(file, p.TypesInfo, coverage)
			}
			return nil
		})

	failures := computePQFailures(fixtureReqs, coverage, root)
	require.NotEmpty(t, failures,
		"CONTRACT-PATH-QUERY-COVERAGE-01 reverse self-check: fixture must produce ≥1 missing-reject failure "+
			"(fixture contract has pathParams but no MustRejectPathParam call)")
}

// TestContractPathQueryCoverage01_FixtureParamPartial — reverse self-check for
// the per-param granularity: fixture declares two query params (limit, cursor)
// but only MustRejectQueryParam("limit", ...). The rule MUST flag "cursor" as
// uncovered AND MUST NOT flag "limit". This is the F2 regression guard — the
// previous contract-level boolean form would have silently passed because
// "limit" sets the contract's queryParamCovered=true bucket.
func TestContractPathQueryCoverage01_FixtureParamPartial(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixtureRelDir := filepath.Join("tools", "archtest", "contract_path_query_coverage_fixtures",
		"red_param_partial", "contracts", "http")
	fixtureReqs := buildContractPQRequirementsFromRelDir(t, root, fixtureRelDir)
	require.NotEmpty(t, fixtureReqs, "red_param_partial fixture must have at least one contract")
	// Sanity: ensure the fixture really has both query params we expect.
	var req contractPQParamInfo
	for _, r := range fixtureReqs {
		if r.ID == "http.test.paramcoverage-partial.v1" {
			req = r
			break
		}
	}
	require.NotEmpty(t, req.ID, "fixture must declare http.test.paramcoverage-partial.v1")
	require.ElementsMatch(t, []string{"limit", "cursor"}, req.QueryParamNames,
		"fixture must declare exactly two query params (limit, cursor)")

	coverage := newPQCoverage()
	fixturePattern := "./tools/archtest/contract_path_query_coverage_fixtures/red_param_partial/..."
	_ = Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{fixturePattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				attributePQCoverageFromFile(file, p.TypesInfo, coverage)
			}
			return nil
		})

	failures := computePQFailures([]contractPQParamInfo{req}, coverage, root)
	require.Len(t, failures, 1,
		"reverse self-check: exactly one param (cursor) should be flagged uncovered; got: %v", failures)
	assert := require.New(t)
	assert.Contains(failures[0].Message, "cursor",
		"missing failure must reference param name `cursor`; got: %s", failures[0].Message)
	assert.NotContains(failures[0].Message, `param "limit"`,
		"covered param `limit` must NOT be flagged; got: %s", failures[0].Message)
}

// TestContractPathQueryParamNameLiteral01_RedComputedParamName — reverse
// self-check: fixture passes a runtime variable as the param-name argument
// to MustRejectQueryParam. The rule MUST flag it.
func TestContractPathQueryParamNameLiteral01_RedComputedParamName(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/contract_path_query_coverage_fixtures/red_param_name_computed/..."
	diags := Run(
		t, Fixture(
			FixtureOpts{Tests: true},
			[]string{fixturePattern},
		),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			return scanContractPQParamNameViolations(p)
		},
	)

	require.NotEmpty(t, diags,
		"CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01 reverse self-check: fixture must produce ≥1 violation "+
			"(fixture calls MustRejectQueryParam with a runtime variable as param name)")
}
