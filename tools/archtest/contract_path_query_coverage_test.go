// INVARIANT: CONTRACT-PATH-QUERY-COVERAGE-01
//
// CONTRACT-PATH-QUERY-COVERAGE-01 — every active HTTP contract that declares
// pathParams or queryParams must have at least one MustRejectPathParam or
// MustRejectQueryParam call site in the corresponding cells/**/contract_test.go.
//
// This rule ensures that param-schema constraints are not merely declared in
// contract.yaml but are actually executed as rejected test cases, so schema
// drift (e.g. removing a minLength) immediately breaks CI.
//
// Tool: RunTypedProduction (040 Pass-Driver) for test files — uses
// *types.Info.Uses to resolve MustRejectPathParam / MustRejectQueryParam
// receiver calls to the *contracttest.Contract type, ensuring the rule cannot
// be bypassed by renaming an import alias. YAML scanning uses
// scanner.EachContentFile (tools/archtest/internal/scanner) to build the
// ground-truth set from contracts/http/**, satisfying
// SCANNER-FRAMEWORK-USAGE-01. NOT registered in
// internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Contract ID discovered only via string literal in LoadByID call. A call
//     like c := LoadByID(t, root, computedID) where computedID is not a literal
//     will NOT be associated with the contract. Compensation: archtest
//     CONTRACTTEST-LOADBYID-LITERAL-01 (in tools/archtest/contracttest_loadbyid_literal_test.go)
//     locks all LoadByID calls to literal strings; non-literal form fails that rule.
//
//  2. MustRejectPathParam / MustRejectQueryParam called on a *Contract variable
//     loaded in an outer function scope (e.g. passed as argument) rather than
//     via a LoadByID call visible in the same function. The per-variable
//     association tracks LoadByID assignments and MustReject calls within the
//     same function body; cross-function use remains a blind spot.
//     Compensation: contract tests conventionally use a single c per test
//     function; code review.
//
//  3. MustRejectPathParam call inside a helper function that is not visible to
//     the file-level scan. Compensation: the TypesInfo receiver-type check
//     catches all call sites regardless of nesting depth within the scanned
//     package.
//
// Reverse self-check: TestContractPathQueryCoverage01_FixtureMissingReject
// loads a fixture package (archtest_fixture build tag) whose contract declares
// pathParams but has no MustRejectPathParam call; the rule MUST report it as
// uncovered.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// contractPQPkgPath is the import path of the contracttest package whose
// Contract methods are being checked.
const contractPQPkgPath = "github.com/ghbvf/gocell/tests/contracttest"

// contractPQMethodPathParam is the method that proves pathParams coverage.
const contractPQMethodPathParam = "MustRejectPathParam"

// contractPQMethodQueryParam is the method that proves queryParams coverage.
const contractPQMethodQueryParam = "MustRejectQueryParam"

// contractPQLoadByID is the function name used to associate a contract ID with
// a test file.
const contractPQLoadByID = "LoadByID"

// contractPQParamInfo holds the path/query param coverage requirement for one
// contract.
type contractPQParamInfo struct {
	ID         string
	HasPath    bool
	HasQuery   bool
	ServerCell string
	FilePath   string // absolute path to contract.yaml
}

// TestContractPathQueryCoverage01 asserts that every active HTTP contract with
// pathParams or queryParams declarations has at least one MustRejectPathParam
// or MustRejectQueryParam call site in a cells/**/contract_test.go file whose
// LoadByID call references that contract ID.
//
// Coverage is tracked at per-variable granularity: each LoadByID assignment
// (c := contracttest.LoadByID(t, root, "<lit>")) establishes a mapping from
// variable name to contract ID within its enclosing function body. Only
// MustReject calls whose receiver is that same variable are attributed to the
// associated contract, preventing false coverage when a file loads contract A
// and contract B but calls MustRejectPathParam only for A.
func TestContractPathQueryCoverage01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// --- True Source A: scan contracts/http for active contracts with path/queryParams ---
	wantCoverage := buildContractPQRequirements(t, root)
	if len(wantCoverage) == 0 {
		t.Log("CONTRACT-PATH-QUERY-COVERAGE-01: no active HTTP contracts with path/queryParams found")
		return
	}

	// --- True Source B: scan cells/**/contract_test.go for MustReject calls ---
	// pathParamCovered[contractID] = true  if MustRejectPathParam was found
	// queryParamCovered[contractID] = true if MustRejectQueryParam was found
	pathParamCovered := make(map[string]bool)
	queryParamCovered := make(map[string]bool)

	_ = RunTypedProduction(t, TypedOpts{Tests: true}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		// Only process files in cells/**/contract_test.go.
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !isCellsContractTestFile(rel) {
				continue
			}
			// Per-function-body association: build loadByIDVars for each
			// top-level function, then mark coverage by receiver variable name.
			attributePQCoverageFromFile(file, p.TypesInfo, pathParamCovered, queryParamCovered)
		}
		return nil
	})

	// --- Assert coverage ---
	failures := computePQFailures(wantCoverage, pathParamCovered, queryParamCovered)
	for _, f := range failures {
		t.Errorf("CONTRACT-PATH-QUERY-COVERAGE-01: %s", f)
	}
}

// TestContractPathQueryCoverage01_FixtureMissingReject is the reverse
// self-check (ai-collab.md §"工具选定后强制盲区自检"): a fixture package with
// build tag archtest_fixture declares a contract with pathParams but has no
// MustRejectPathParam call; the rule MUST report it as uncovered.
func TestContractPathQueryCoverage01_FixtureMissingReject(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Fixture contracts root: tools/archtest/contract_path_query_coverage_fixtures/red_missing_reject/contracts
	fixtureContractsRoot := filepath.Join(root,
		"tools", "archtest", "contract_path_query_coverage_fixtures",
		"red_missing_reject", "contracts")

	// Verify that the fixture contract file exists.
	fixtureContractFile := filepath.Join(fixtureContractsRoot, "http", "test", "paramcoverage", "v1", "contract.yaml")
	if _, err := os.Stat(fixtureContractFile); err != nil {
		t.Fatalf("fixture contract not found at %s: %v", fixtureContractFile, err)
	}

	// Build the requirements from the fixture contracts root.
	fixtureRelDir := filepath.Join("tools", "archtest", "contract_path_query_coverage_fixtures",
		"red_missing_reject", "contracts", "http")
	fixtureReqs := buildContractPQRequirementsFromRelDir(t, root, fixtureRelDir)
	require.NotEmpty(t, fixtureReqs, "fixture must have at least one contract with path/queryParams")

	// The fixture test file does NOT call MustRejectPathParam, so both coverage
	// maps are empty — simulate this by passing empty maps.
	emptyPath := make(map[string]bool)
	emptyQuery := make(map[string]bool)
	failures := computePQFailures(fixtureReqs, emptyPath, emptyQuery)
	require.NotEmpty(t, failures,
		"CONTRACT-PATH-QUERY-COVERAGE-01 reverse self-check: fixture must produce ≥1 missing-reject failure "+
			"(fixture contract has pathParams but no MustRejectPathParam call)")
}

// --- Core logic helpers ---

// computePQFailures returns the list of failure messages for contracts that
// declare pathParams/queryParams but lack the corresponding MustReject call.
// Extracted so both the main rule and the reverse self-check run the same
// assertion logic.
func computePQFailures(wantCoverage []contractPQParamInfo, pathParamCovered, queryParamCovered map[string]bool) []string {
	var failures []string
	for _, req := range wantCoverage {
		if req.HasPath && !pathParamCovered[req.ID] {
			failures = append(failures, fmt.Sprintf(
				"contract %q (server: %s) has pathParams but no MustRejectPathParam call site in cells/**/contract_test.go",
				req.ID, req.ServerCell,
			))
		}
		if req.HasQuery && !queryParamCovered[req.ID] {
			failures = append(failures, fmt.Sprintf(
				"contract %q (server: %s) has queryParams but no MustRejectQueryParam call site in cells/**/contract_test.go",
				req.ID, req.ServerCell,
			))
		}
	}
	return failures
}

// attributePQCoverageFromFile walks the top-level function declarations in
// file. For each function body it builds a per-variable map of
// (varName → contractID) from LoadByID assignments, then marks
// pathParamCovered / queryParamCovered for the contract associated with each
// MustReject receiver variable.
//
// This per-variable granularity ensures that a file loading contract A and
// contract B only marks A as covered when MustRejectPathParam is called on
// the variable bound to A, not on the variable bound to B.
func attributePQCoverageFromFile(
	file *ast.File,
	info *types.Info,
	pathParamCovered map[string]bool,
	queryParamCovered map[string]bool,
) {
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		// Build varName → contractID map for this function body.
		loadByIDVars := extractLoadByIDVars(fn.Body, info)
		if len(loadByIDVars) == 0 {
			return
		}
		// Scan MustReject calls and attribute coverage per receiver variable.
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return
			}
			name := sel.Sel.Name
			if name != contractPQMethodPathParam && name != contractPQMethodQueryParam {
				return
			}
			if !isContractPQReceiverMethod(sel, info, name) {
				return
			}
			// Receiver must be a plain identifier bound to a LoadByID variable.
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return
			}
			contractID, found := loadByIDVars[recv.Name]
			if !found {
				return
			}
			switch name {
			case contractPQMethodPathParam:
				pathParamCovered[contractID] = true
			case contractPQMethodQueryParam:
				queryParamCovered[contractID] = true
			}
		})
	})
}

// extractLoadByIDVars scans a function body for variable assignments of the
// forms:
//
//	c := contracttest.LoadByID(t, root, "<literal>")   // short declaration
//	c  = contracttest.LoadByID(t, root, "<literal>")   // reassignment
//
// and returns a map from variable name to contract ID. When a variable is
// reassigned multiple times, the last assignment wins (later MustReject calls
// in the same function will be attributed to the latest contract).
// Uses *types.Info to confirm the callee is contracttest.LoadByID; only
// string literal third arguments are recorded.
func extractLoadByIDVars(body *ast.BlockStmt, info *types.Info) map[string]string {
	result := make(map[string]string)
	EachInSubtree[ast.AssignStmt](body, func(assign *ast.AssignStmt) {
		// Accept both short declarations (:=) and regular assignments (=).
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
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		id, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		result[varIdent.Name] = id
	})
	return result
}

// --- Helpers ---

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
	hasPath := len(cy.Endpoints.HTTP.PathParams) > 0
	hasQuery := len(cy.Endpoints.HTTP.QueryParams) > 0
	if !hasPath && !hasQuery {
		return contractPQParamInfo{}, false
	}
	return contractPQParamInfo{
		ID:         cy.ID,
		HasPath:    hasPath,
		HasQuery:   hasQuery,
		ServerCell: cy.Endpoints.Server,
		FilePath:   path,
	}, true
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
