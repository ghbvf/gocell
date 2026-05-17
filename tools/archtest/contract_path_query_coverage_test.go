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
// Tool: RunTypedProduction (040 Pass-Driver) for test files — uses
// *types.Info.Uses to resolve MustReject{Path,Query}Param / Validate{Path,Query}Param
// receiver calls to the *contracttest.Contract type, and
// typeseval.EvaluateConstString to fold const idents / selectors / binary
// expressions in argument positions. YAML scanning uses
// scanner.EachContentFile (tools/archtest/internal/scanner) to build the
// ground-truth set from contracts/http/**, satisfying
// SCANNER-FRAMEWORK-USAGE-01. NOT registered in
// internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
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
// AI Hard upgrade backlog: cap-14
// CONTRACT-PATH-QUERY-COVERAGE-HARD-CODEGEN-01 — derive each MustReject*Param
// stub from contract.yaml at codegen time so a missing param is an
// unrepresentable build outcome.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// contractPQPkgPath is the import path of the contracttest package whose
// Contract methods are being checked.
const contractPQPkgPath = "github.com/ghbvf/gocell/tests/contracttest"

// Method names locked by this rule. ValidateXxxParam is also tracked for
// PARAM-NAME-LITERAL-01 (a runtime-named ValidateXxxParam call is the same
// kind of bypass risk as a runtime-named MustRejectXxxParam call).
const (
	contractPQMethodMustRejectPath  = "MustRejectPathParam"
	contractPQMethodMustRejectQuery = "MustRejectQueryParam"
	contractPQMethodValidatePath    = "ValidatePathParam"
	contractPQMethodValidateQuery   = "ValidateQueryParam"
)

// contractPQLoadByID is the function name used to associate a contract ID with
// a test file.
const contractPQLoadByID = "LoadByID"

// pqParamKind tags a coverage entry as path or query.
type pqParamKind string

const (
	pqKindPath  pqParamKind = "pathParam"
	pqKindQuery pqParamKind = "queryParam"
)

// contractPQParamInfo holds the per-param coverage requirement for one contract.
// PathParamNames / QueryParamNames are the sorted list of declared param names
// (from contract.yaml endpoints.http.{path,query}Params keys); each name is an
// independent coverage obligation.
type contractPQParamInfo struct {
	ID              string
	PathParamNames  []string
	QueryParamNames []string
	ServerCell      string
	FilePath        string // absolute path to contract.yaml
}

// pqCoverage records which (contractID, paramKind, paramName) tuples were
// covered by at least one MustReject{Path,Query}Param call.
type pqCoverage struct {
	path  map[string]map[string]bool // path[contractID][paramName] = true
	query map[string]map[string]bool // query[contractID][paramName] = true
}

func newPQCoverage() *pqCoverage {
	return &pqCoverage{
		path:  make(map[string]map[string]bool),
		query: make(map[string]map[string]bool),
	}
}

func (c *pqCoverage) mark(kind pqParamKind, contractID, paramName string) {
	target := c.path
	if kind == pqKindQuery {
		target = c.query
	}
	inner, ok := target[contractID]
	if !ok {
		inner = make(map[string]bool)
		target[contractID] = inner
	}
	inner[paramName] = true
}

func (c *pqCoverage) covered(kind pqParamKind, contractID, paramName string) bool {
	target := c.path
	if kind == pqKindQuery {
		target = c.query
	}
	return target[contractID][paramName]
}

// TestContractPathQueryCoverage01 asserts that every (contractID, paramKind,
// paramName) tuple declared in an active HTTP contract has at least one
// MustReject{Path,Query}Param call site in a cells/**/contract_test.go file
// whose LoadByID variable is bound to that contract ID and whose second
// argument resolves (via EvaluateConstString) to that paramName.
func TestContractPathQueryCoverage01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	wantCoverage := buildContractPQRequirements(t, root)
	if len(wantCoverage) == 0 {
		t.Log("CONTRACT-PATH-QUERY-COVERAGE-01: no active HTTP contracts with path/queryParams found")
		return
	}

	coverage := newPQCoverage()
	_ = RunTypedProduction(t, TypedOpts{Tests: true}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !isCellsContractTestFile(rel) {
				continue
			}
			attributePQCoverageFromFile(file, p.TypesInfo, coverage)
		}
		return nil
	})

	failures := computePQFailures(wantCoverage, coverage)
	for _, f := range failures {
		t.Errorf("CONTRACT-PATH-QUERY-COVERAGE-01: %s", f)
	}
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
	_ = RunTypedFixture(t, FixtureOpts{Tests: true},
		[]string{fixturePattern}, func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				attributePQCoverageFromFile(file, p.TypesInfo, coverage)
			}
			return nil
		})

	failures := computePQFailures(fixtureReqs, coverage)
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
	_ = RunTypedFixture(t, FixtureOpts{Tests: true},
		[]string{fixturePattern}, func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				attributePQCoverageFromFile(file, p.TypesInfo, coverage)
			}
			return nil
		})

	failures := computePQFailures([]contractPQParamInfo{req}, coverage)
	require.Len(t, failures, 1,
		"reverse self-check: exactly one param (cursor) should be flagged uncovered; got: %v", failures)
	assert := require.New(t)
	assert.Contains(failures[0], "cursor",
		"missing failure must reference param name `cursor`; got: %s", failures[0])
	assert.NotContains(failures[0], `param "limit"`,
		"covered param `limit` must NOT be flagged; got: %s", failures[0])
}

// TestContractPathQueryParamNameLiteral01 asserts that every Validate{Path,Query}Param
// / MustReject{Path,Query}Param call site supplies a compile-time constant
// string as the param-name argument (call.Args[0]). Runtime expressions are
// rejected via typeseval.EvaluateConstString — same enforcement shape as
// CONTRACTTEST-LOADBYID-LITERAL-01.
func TestContractPathQueryParamNameLiteral01(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{Tests: true}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		return scanContractPQParamNameViolations(p)
	})

	Report(t, "CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01", diags)
}

// TestContractPathQueryParamNameLiteral01_RedComputedParamName — reverse
// self-check: fixture passes a runtime variable as the param-name argument
// to MustRejectQueryParam. The rule MUST flag it.
func TestContractPathQueryParamNameLiteral01_RedComputedParamName(t *testing.T) {
	t.Parallel()

	fixturePattern := "./tools/archtest/contract_path_query_coverage_fixtures/red_param_name_computed/..."
	diags := RunTypedFixture(t, FixtureOpts{Tests: true},
		[]string{fixturePattern},
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

// scanContractPQParamNameViolations walks every call to a *contracttest.Contract
// Validate/MustReject{Path,Query}Param method and emits a diagnostic when the
// first argument (the param name) does not resolve to a compile-time const.
func scanContractPQParamNameViolations(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return
			}
			name := sel.Sel.Name
			if !isContractPQParamMethodName(name) {
				return
			}
			if !isContractPQReceiverMethod(sel, p.TypesInfo, name) {
				return
			}
			if len(call.Args) < 2 {
				return
			}
			// call signature: Validate/MustReject*Param(t, paramName, value)
			// arg[1] is paramName.
			if _, ok := EvaluateConstString(p.TypesInfo, call.Args[1]); ok {
				return // compliant
			}
			pos := p.Fset.Position(call.Args[1].Pos())
			diags = append(diags, Diagnostic{
				Rel:  p.Rel(file),
				Line: pos.Line,
				Message: fmt.Sprintf(
					"CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01: %s param-name argument must be a compile-time constant string; got runtime expression",
					name,
				),
			})
		})
	}
	return diags
}

// --- Core logic helpers ---

// computePQFailures returns one failure per uncovered (contractID, paramKind,
// paramName) tuple. The per-param granularity is the F2 fix.
func computePQFailures(wantCoverage []contractPQParamInfo, coverage *pqCoverage) []string {
	var failures []string
	for _, req := range wantCoverage {
		for _, name := range req.PathParamNames {
			if !coverage.covered(pqKindPath, req.ID, name) {
				failures = append(failures, fmt.Sprintf(
					"contract %q (server: %s) pathParam %q has no MustRejectPathParam call site in cells/**/contract_test.go",
					req.ID, req.ServerCell, name,
				))
			}
		}
		for _, name := range req.QueryParamNames {
			if !coverage.covered(pqKindQuery, req.ID, name) {
				failures = append(failures, fmt.Sprintf(
					"contract %q (server: %s) queryParam %q has no MustRejectQueryParam call site in cells/**/contract_test.go",
					req.ID, req.ServerCell, name,
				))
			}
		}
	}
	return failures
}

// attributePQCoverageFromFile walks the top-level function declarations in
// file. For each function body it builds a per-variable map of
// (varName → contractID) from LoadByID assignments, then marks coverage per
// (contractID, paramKind, paramName) for each MustReject* call where the
// receiver variable is bound to a known contract and the first argument
// resolves to a const string param name.
func attributePQCoverageFromFile(file *ast.File, info *types.Info, coverage *pqCoverage) {
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		loadByIDVars := extractLoadByIDVars(fn.Body, info)
		if len(loadByIDVars) == 0 {
			return
		}
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return
			}
			name := sel.Sel.Name
			var kind pqParamKind
			switch name {
			case contractPQMethodMustRejectPath:
				kind = pqKindPath
			case contractPQMethodMustRejectQuery:
				kind = pqKindQuery
			default:
				return
			}
			if !isContractPQReceiverMethod(sel, info, name) {
				return
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return
			}
			contractID, found := loadByIDVars[recv.Name]
			if !found {
				return
			}
			if len(call.Args) < 2 {
				return
			}
			// Param name (arg[1]) must resolve to a const string. If it does
			// not, PARAM-NAME-LITERAL-01 reports it independently; here we
			// simply skip — silently marking unknown-name coverage would
			// re-introduce the F2 contract-level boolean bypass.
			paramName, ok := EvaluateConstString(info, call.Args[1])
			if !ok {
				return
			}
			coverage.mark(kind, contractID, paramName)
		})
	})
}

// extractLoadByIDVars scans a function body for LoadByID variable assignments
// and returns a map from variable name to contract ID. Reassignment: last
// wins. Uses EvaluateConstString (single-source const evaluator) for the
// third argument, matching CONTRACTTEST-LOADBYID-LITERAL-01's accepted form.
func extractLoadByIDVars(body *ast.BlockStmt, info *types.Info) map[string]string {
	result := make(map[string]string)
	EachInSubtree[ast.AssignStmt](body, func(assign *ast.AssignStmt) {
		if assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN {
			return
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return
		}
		varIdent, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return
		}
		if !isContractPQFunc(call.Fun, info, contractPQLoadByID) {
			return
		}
		id, ok := EvaluateConstString(info, call.Args[2])
		if !ok {
			return
		}
		result[varIdent.Name] = id
	})
	return result
}

// --- Helpers ---

// isContractPQParamMethodName returns true for the four method names that
// PARAM-NAME-LITERAL-01 governs.
func isContractPQParamMethodName(name string) bool {
	switch name {
	case contractPQMethodMustRejectPath, contractPQMethodMustRejectQuery,
		contractPQMethodValidatePath, contractPQMethodValidateQuery:
		return true
	}
	return false
}

// buildContractPQRequirements scans contracts/http/**  in the module root.
func buildContractPQRequirements(t *testing.T, moduleRoot string) []contractPQParamInfo {
	t.Helper()
	return buildContractPQRequirementsFromRelDir(t, moduleRoot, filepath.Join("contracts", "http"))
}

// buildContractPQRequirementsFromRelDir scans <moduleRoot>/<relDir>/** for
// active HTTP contracts that declare pathParams or queryParams. Uses
// scanner.EachContentFile (SCANNER-FRAMEWORK-USAGE-01 funnel) for YAML
// discovery so the rule cannot bypass the framework via os/filepath.Walk.
func buildContractPQRequirementsFromRelDir(t *testing.T, moduleRoot, relDir string) []contractPQParamInfo {
	t.Helper()
	var result []contractPQParamInfo
	scope := scanner.DirsScope(moduleRoot, []string{relDir},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		t.Helper()
		req, ok := parseContractPQRequirementFromBytes(t, fc.AbsPath, fc.Bytes)
		if ok {
			result = append(result, req)
		}
	})
	return result
}

// contractPQYAML is a minimal YAML struct for extracting the fields we need.
type contractPQYAML struct {
	ID        string `yaml:"id"`
	Lifecycle string `yaml:"lifecycle"`
	Endpoints struct {
		Server string `yaml:"server"`
		HTTP   *struct {
			PathParams  map[string]any `yaml:"pathParams"`
			QueryParams map[string]any `yaml:"queryParams"`
		} `yaml:"http"`
	} `yaml:"endpoints"`
}

// parseContractPQRequirementFromBytes parses one contract.yaml byte buffer and
// returns a requirement if the contract is active and has path/queryParams.
// Both param-name lists are sorted for stable diagnostic ordering.
func parseContractPQRequirementFromBytes(t *testing.T, path string, data []byte) (contractPQParamInfo, bool) {
	t.Helper()
	var cy contractPQYAML
	if err := yaml.Unmarshal(data, &cy); err != nil {
		t.Fatalf("CONTRACT-PATH-QUERY-COVERAGE-01: parse %s: %v", path, err)
	}
	if cy.Lifecycle != "active" {
		return contractPQParamInfo{}, false
	}
	if cy.Endpoints.HTTP == nil {
		return contractPQParamInfo{}, false
	}
	pathNames := sortedMapKeys(cy.Endpoints.HTTP.PathParams)
	queryNames := sortedMapKeys(cy.Endpoints.HTTP.QueryParams)
	if len(pathNames) == 0 && len(queryNames) == 0 {
		return contractPQParamInfo{}, false
	}
	return contractPQParamInfo{
		ID:              cy.ID,
		PathParamNames:  pathNames,
		QueryParamNames: queryNames,
		ServerCell:      cy.Endpoints.Server,
		FilePath:        path,
	}, true
}

func sortedMapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isCellsContractTestFile reports whether the module-relative slash path
// matches cells/**/contract_test.go.
func isCellsContractTestFile(rel string) bool {
	return strings.HasPrefix(rel, "cells/") &&
		filepath.Base(rel) == "contract_test.go"
}

// isContractPQReceiverMethod reports whether sel is a method call on
// *contracttest.Contract with the given method name.
func isContractPQReceiverMethod(sel *ast.SelectorExpr, info *types.Info, methodName string) bool {
	if info == nil {
		return sel.Sel.Name == methodName
	}
	obj := info.Uses[sel.Sel]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == contractPQPkgPath && fn.Name() == methodName
}

// isContractPQFunc reports whether funExpr refers to contracttest.<funcName>.
// Caller must always supply non-nil TypesInfo; info == nil returns false
// without attempting an AST-string fallback (which would be an unsafe
// security downgrade — a method named identically on a different type would
// pass the check).
func isContractPQFunc(funExpr ast.Expr, info *types.Info, funcName string) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	if sel.Sel.Name != funcName {
		return false
	}
	if info == nil {
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
	return fn.Pkg().Path() == contractPQPkgPath && fn.Name() == funcName
}
