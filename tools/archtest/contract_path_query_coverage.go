package archtest

// contract_path_query_coverage.go — importable
// CONTRACT-PATH-QUERY-COVERAGE-01 and
// CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01 rule logic (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run these rules; Go
// never compiles a dependency's _test.go. GoCell's own Test* in
// contract_path_query_coverage_test.go dogfood the same Check* (single
// source, no parallel rule body).
//
// # CONTRACT-PATH-QUERY-COVERAGE-01
//
// Every active HTTP contract that declares pathParams or queryParams must have
// at least one MustRejectPathParam or MustRejectQueryParam call site in the
// corresponding cells/**/contract_test.go for EVERY declared param name.
// Coverage is tracked per (contractID, kind, paramName); a contract declaring
// three query params with only one MustRejectQueryParam call site fires two
// diagnostics (one per uncovered param).
//
// # CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01
//
// The second argument to every Validate{Path,Query}Param /
// MustReject{Path,Query}Param call must resolve to a compile-time constant
// string via EvaluateConstString. Without this, a runtime variable as param
// name would let the COVERAGE-01 attribution mistake the abstract variable
// name for a covered parameter.
//
// # AI-robust grade
//
// Medium. Tool: Run(t, Production(TypedOpts{Tests: true})) — uses
// *types.Info.Uses to resolve method calls to the *contracttest.Contract type,
// and EvaluateConstString to fold const idents / selectors / binary expressions
// in argument positions. YAML scanning uses scanner.EachContentFile.
//
// # Not registered (register=none)
//
// Not registered in StandardCellRules: gocell-internal-layout funnel — the
// rules lock the contracttest package path (under the GoCell module) and scan
// contracts/http/** which do not exist in an external module. Running against
// an external repo produces vacuous-green or false-red. Enforced in GoCell via
// TestContractPathQueryCoverage01 and TestContractPathQueryParamNameLiteral01.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// contractPQPkgPath is the import path of the contracttest package whose
// Contract methods are being checked. Derived from PlatformModulePath so a
// module rename updates exactly one place (ARCHTEST-MODULE-PATH-FUNNEL-01).
const contractPQPkgPath = PlatformModulePath + "/tests/contracttest"

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

// contractPQParamInfo holds the per-param coverage requirement for one
// contract. PathParamNames / QueryParamNames are the sorted list of declared
// param names (from contract.yaml endpoints.http.{path,query}Params keys);
// each name is an independent coverage obligation.
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

// CheckContractPathQueryCoverage01 runs CONTRACT-PATH-QUERY-COVERAGE-01 over
// the running module and returns diagnostics. It does NOT call t.Errorf; the
// caller should funnel results through
// Report(t, "CONTRACT-PATH-QUERY-COVERAGE-01", ...).
//
// Not registered in StandardCellRules: gocell-internal-layout funnel; vacuous-
// green/false-red in an external module; enforced in GoCell via
// TestContractPathQueryCoverage01.
func CheckContractPathQueryCoverage01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	wantCoverage := buildContractPQRequirements(t, root)
	if len(wantCoverage) == 0 {
		return nil
	}

	coverage := newPQCoverage()
	_ = Run(t, Production(TypedOpts{Tests: true}), func(p *Pass) []Diagnostic {
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
	diags := make([]Diagnostic, 0, len(failures))
	for _, f := range failures {
		diags = append(diags, Diagnostic{Message: f})
	}
	return diags
}

// CheckContractPathQueryParamNameLiteral01 runs
// CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01 over the running module and
// returns diagnostics. It does NOT call t.Errorf; the caller should funnel
// results through
// Report(t, "CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01", ...).
//
// Not registered in StandardCellRules: gocell-internal-layout funnel; vacuous-
// green/false-red in an external module; enforced in GoCell via
// TestContractPathQueryParamNameLiteral01.
func CheckContractPathQueryParamNameLiteral01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Production(TypedOpts{Tests: true}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		return scanContractPQParamNameViolations(p)
	})
}

// scanContractPQParamNameViolations walks every call to a *contracttest.Contract
// Validate/MustReject{Path,Query}Param method and emits a diagnostic when the
// first argument (the param name) does not resolve to a compile-time const.
func scanContractPQParamNameViolations(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			diags = append(diags, pqParamNameViolation(p, file, call)...)
		})
	}
	return diags
}

// pqParamNameViolation returns a diagnostic when call is a
// Validate/MustReject{Path,Query}Param method whose param-name argument (arg[1])
// does not resolve to a compile-time const string, else nil.
func pqParamNameViolation(p *Pass, file *ast.File, call *ast.CallExpr) []Diagnostic {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return nil
	}
	name := sel.Sel.Name
	if !isContractPQParamMethodName(name) {
		return nil
	}
	if !isContractPQReceiverMethod(sel, p.TypesInfo, name) {
		return nil
	}
	if len(call.Args) < 2 {
		return nil
	}
	// call signature: Validate/MustReject*Param(t, paramName, value); arg[1] is paramName.
	if _, ok := EvaluateConstString(p.TypesInfo, call.Args[1]); ok {
		return nil // compliant
	}
	pos := p.Fset.Position(call.Args[1].Pos())
	return []Diagnostic{{
		Rel:  p.Rel(file),
		Line: pos.Line,
		Message: fmt.Sprintf(
			"CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01: %s param-name argument must be a compile-time constant string; got runtime expression",
			name,
		),
	}}
}

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
			markPQCoverageFromCall(call, loadByIDVars, info, coverage)
		})
	})
}

// markPQCoverageFromCall marks (paramKind, contractID, paramName) coverage when
// call is a MustReject{Path,Query}Param invocation on a LoadByID-bound receiver
// with a const-string param name; non-matching calls are ignored. A non-const
// param name is skipped here (PARAM-NAME-LITERAL-01 reports it independently;
// silently marking unknown-name coverage would re-introduce the F2
// contract-level boolean bypass).
func markPQCoverageFromCall(call *ast.CallExpr, loadByIDVars map[string]string, info *types.Info, coverage *pqCoverage) {
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
	paramName, ok := EvaluateConstString(info, call.Args[1])
	if !ok {
		return
	}
	coverage.mark(kind, contractID, paramName)
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
	scope := scanner.DirsScope(
		moduleRoot, []string{relDir},
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
		return false // fail-closed: cannot confirm receiver type without type info
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
