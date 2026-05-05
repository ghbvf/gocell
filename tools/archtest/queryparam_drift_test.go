package archtest

// queryparam_drift_test.go - static archtest for PR-MODE-3 META-QUERYPARAM-DRIFT.
//
// The rule compares query parameters consumed by HTTP handlers/policies against
// the endpoint's contract.yaml declaration. The contract is the source of truth;
// every query parameter read from r.URL.Query().Get("...") or from the shared
// pagination parser must appear under endpoints.http.queryParams, and declared
// query params must be consumed by the mounted handler or policy.
//
// ref: kubernetes/pkg/apis/core/validation/validation.go - collect all field
//      errors with precise field paths instead of short-circuiting.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/query"
)

const queryParamDriftRule = "META-QUERYPARAM-DRIFT-01"

type queryParamContract struct {
	ID        string `yaml:"id"`
	Endpoints struct {
		HTTP struct {
			QueryParams map[string]queryParamSchema `yaml:"queryParams"`
		} `yaml:"http"`
	} `yaml:"endpoints"`
	File string `yaml:"-"`
}

type queryParamSchema struct {
	Type      string `yaml:"type"`
	Format    string `yaml:"format"`
	Required  bool   `yaml:"required"`
	MinLength *int   `yaml:"minLength"`
	MaxLength *int   `yaml:"maxLength"`
	Minimum   *int   `yaml:"minimum"`
	Maximum   *int   `yaml:"maximum"`
}

type routeQueryBinding struct {
	ContractID string
	File       string
	Line       int
	Funcs      []string
}

type queryParamViolation struct {
	ContractID string
	File       string
	Line       int
	Param      string
	Kind       string
}

func (v queryParamViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: contract %s %s query param %q",
		queryParamDriftRule, v.File, v.Line, v.ContractID, v.Kind, v.Param)
}

func TestQueryParamContractDrift(t *testing.T) {
	root := findModuleRoot(t)

	violations, err := checkQueryParamContractDrift(root)
	require.NoError(t, err)

	for _, v := range violations {
		t.Log(v.String())
	}
	assert.Empty(t, violations,
		"META-QUERYPARAM-DRIFT-01: HTTP handlers/policies and contract.yaml "+
			"must declare the same query params. Add missing endpoints.http.queryParams "+
			"entries or remove stale declarations.")
}

func checkQueryParamContractDrift(root string) ([]queryParamViolation, error) {
	contracts, err := loadHTTPQueryParamContracts(root)
	if err != nil {
		return nil, err
	}
	bindings, usedByFunc, err := collectRouteQueryBindings(root)
	if err != nil {
		return nil, err
	}
	return compareQueryParams(contracts, bindings, usedByFunc), nil
}

func loadHTTPQueryParamContracts(root string) (map[string]queryParamContract, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, err
	}
	out := map[string]queryParamContract{}
	for _, c := range project.Contracts {
		if c.Kind != "http" {
			continue
		}
		path := filepath.Join(root, c.File)
		parsed, err := parseQueryParamContract(path)
		if err != nil {
			return nil, err
		}
		out[parsed.ID] = parsed
	}
	return out, nil
}

func parseQueryParamContract(path string) (queryParamContract, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return queryParamContract{}, err
	}
	var c queryParamContract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return queryParamContract{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c.File = filepath.ToSlash(path)
	return c, nil
}

func collectRouteQueryBindings(root string) ([]routeQueryBinding, map[string]map[string]struct{}, error) {
	files, err := findQueryParamScanFiles(root)
	if err != nil {
		return nil, nil, err
	}
	var bindings []routeQueryBinding
	usedByFunc := map[string]map[string]struct{}{}
	for _, file := range files {
		fileBindings, fileUsed, err := scanQueryParamFile(root, file)
		if err != nil {
			return nil, nil, err
		}
		bindings = append(bindings, fileBindings...)
		maps.Copy(usedByFunc, fileUsed)
	}
	return bindings, usedByFunc, nil
}

func findQueryParamScanFiles(root string) ([]string, error) {
	return findCellProductionGoFiles(root)
}

func scanQueryParamFile(root, path string) ([]routeQueryBinding, map[string]map[string]struct{}, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, nil, err
	}
	rel = filepath.ToSlash(rel)
	specIDs := collectContractSpecIDs(file)
	return collectRouteBindings(fset, file, rel, specIDs), collectQueryParamUses(file, rel), nil
}

func TestScanQueryParamFile_ParseErrorFailsClosed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cells", "badcell", "slices", "bad", "handler.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("package bad\nfunc broken("), 0o644))

	_, _, err := scanQueryParamFile(root, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
	assert.Contains(t, err.Error(), filepath.ToSlash(path))
}

func collectContractSpecIDs(file *ast.File) map[string]string {
	specs := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			collectValueSpecContractIDs(specs, valueSpec)
		}
	}
	return specs
}

func collectValueSpecContractIDs(out map[string]string, spec *ast.ValueSpec) {
	for i, name := range spec.Names {
		if i >= len(spec.Values) {
			continue
		}
		if id, ok := contractSpecIDFromExpr(spec.Values[i]); ok {
			out[name.Name] = id
		}
	}
}

func collectRouteBindings(fset *token.FileSet, file *ast.File, rel string, specIDs map[string]string) []routeQueryBinding {
	var bindings []routeQueryBinding
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isAuthRouteLiteral(lit) {
			return true
		}
		binding, ok := routeBindingFromLiteral(fset, lit, rel, specIDs)
		if ok {
			bindings = append(bindings, binding)
		}
		return true
	})
	return bindings
}

func routeBindingFromLiteral(fset *token.FileSet, lit *ast.CompositeLit, rel string, specIDs map[string]string) (routeQueryBinding, bool) {
	var contractID string
	var funcs []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Contract":
			contractID, _ = routeContractID(kv.Value, specIDs)
		case "Handler", "Policy":
			funcs = append(funcs, routeFuncKeys(rel, kv.Value)...)
		}
	}
	if contractID == "" || len(funcs) == 0 {
		return routeQueryBinding{}, false
	}
	return routeQueryBinding{
		ContractID: contractID,
		File:       rel,
		Line:       fset.Position(lit.Pos()).Line,
		Funcs:      uniqueStrings(funcs),
	}, true
}

func collectQueryParamUses(file *ast.File, rel string) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		params := collectFuncQueryParams(fn.Body)
		if len(params) > 0 {
			out[funcKey(rel, fn.Name.Name)] = params
		}
	}
	return out
}

func collectFuncQueryParams(body *ast.BlockStmt) map[string]struct{} {
	params := map[string]struct{}{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if queryParam, ok := queryGetParam(call); ok {
			params[queryParam] = struct{}{}
		}
		if isParsePageParamsCall(call) {
			params["cursor"] = struct{}{}
			params["limit"] = struct{}{}
		}
		return true
	})
	return params
}

func compareQueryParams(
	contracts map[string]queryParamContract,
	bindings []routeQueryBinding,
	usedByFunc map[string]map[string]struct{},
) []queryParamViolation {
	var violations []queryParamViolation
	for _, binding := range bindings {
		contract, ok := contracts[binding.ContractID]
		if !ok {
			continue
		}
		declared := stringSetKeys(contract.Endpoints.HTTP.QueryParams)
		used := queryParamsForBinding(binding, usedByFunc)
		violations = append(violations, missingQueryParamViolations(binding, declared, used)...)
		violations = append(violations, staleQueryParamViolations(binding, declared, used)...)
		violations = append(violations, paginationBoundViolations(binding, contract, used)...)
	}
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].String() < violations[j].String()
	})
	return violations
}

func queryParamsForBinding(binding routeQueryBinding, usedByFunc map[string]map[string]struct{}) map[string]struct{} {
	used := map[string]struct{}{}
	for _, fn := range binding.Funcs {
		for param := range usedByFunc[fn] {
			used[param] = struct{}{}
		}
	}
	return used
}

func missingQueryParamViolations(binding routeQueryBinding, declared, used map[string]struct{}) []queryParamViolation {
	var violations []queryParamViolation
	for param := range used {
		if _, ok := declared[param]; !ok {
			violations = append(violations, newQueryParamViolation(binding, param, "uses undeclared"))
		}
	}
	return violations
}

func staleQueryParamViolations(binding routeQueryBinding, declared, used map[string]struct{}) []queryParamViolation {
	var violations []queryParamViolation
	for param := range declared {
		if _, ok := used[param]; !ok {
			violations = append(violations, newQueryParamViolation(binding, param, "declares unused"))
		}
	}
	return violations
}

func newQueryParamViolation(binding routeQueryBinding, param, kind string) queryParamViolation {
	return queryParamViolation{
		ContractID: binding.ContractID,
		File:       binding.File,
		Line:       binding.Line,
		Param:      param,
		Kind:       kind,
	}
}

func paginationBoundViolations(binding routeQueryBinding, contract queryParamContract, used map[string]struct{}) []queryParamViolation {
	var violations []queryParamViolation
	if _, ok := used["cursor"]; ok {
		schema, declared := contract.Endpoints.HTTP.QueryParams["cursor"]
		if declared && (schema.MaxLength == nil || *schema.MaxLength != query.MaxCursorTokenBytes) {
			violations = append(violations, newQueryParamViolation(binding, "cursor", "declares maxLength different from query.MaxCursorTokenBytes"))
		}
	}
	if _, ok := used["limit"]; ok {
		schema, declared := contract.Endpoints.HTTP.QueryParams["limit"]
		if declared && (schema.Maximum == nil || *schema.Maximum != query.MaxPageSize) {
			violations = append(violations, newQueryParamViolation(binding, "limit", "declares maximum different from query.MaxPageSize"))
		}
	}
	return violations
}

func contractSpecIDFromExpr(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || !isContractSpecLiteral(lit) {
		return "", false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "ID" {
			continue
		}
		return stringLiteralValue(kv.Value)
	}
	return "", false
}

func routeContractID(expr ast.Expr, specIDs map[string]string) (string, bool) {
	if ident, ok := expr.(*ast.Ident); ok {
		id, ok := specIDs[ident.Name]
		return id, ok
	}
	return contractSpecIDFromExpr(expr)
}

func routeFuncKeys(rel string, expr ast.Expr) []string {
	switch e := expr.(type) {
	case *ast.Ident:
		return []string{funcKey(rel, e.Name)}
	case *ast.SelectorExpr:
		return []string{funcKey(rel, e.Sel.Name)}
	case *ast.CallExpr:
		return routeFuncKeysFromCall(rel, e)
	default:
		return nil
	}
}

func routeFuncKeysFromCall(rel string, call *ast.CallExpr) []string {
	funcs := routeFuncKeys(rel, call.Fun)
	for _, arg := range call.Args {
		funcs = append(funcs, routeFuncKeys(rel, arg)...)
	}
	return funcs
}

func TestRouteFuncKeysFromCallIncludesCalleeAndArgs(t *testing.T) {
	expr, err := parser.ParseExpr(`wrapPolicy(auditQueryPolicy, h.HandleQuery)`)
	require.NoError(t, err)

	got := routeFuncKeys("cells/auditcore/slices/auditquery/handler.go", expr)

	assert.ElementsMatch(t, []string{
		"cells/auditcore/slices/auditquery/handler.go#wrapPolicy",
		"cells/auditcore/slices/auditquery/handler.go#auditQueryPolicy",
		"cells/auditcore/slices/auditquery/handler.go#HandleQuery",
	}, got)
}

func queryGetParam(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Get" || len(call.Args) != 1 {
		return "", false
	}
	if !isURLQueryCall(sel.X) {
		return "", false
	}
	return stringLiteralValue(call.Args[0])
}

func isURLQueryCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Query"
}

func isParsePageParamsCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if ok && sel.Sel.Name == "ParsePageParamsOrWrite" {
		return true
	}
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "ParsePageParamsOrWrite"
}

func isAuthRouteLiteral(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Route"
}

func isContractSpecLiteral(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "ContractSpec"
}

func stringLiteralValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	val, err := strconv.Unquote(lit.Value)
	return val, err == nil
}

func stringSetKeys[V any](m map[string]V) map[string]struct{} {
	out := map[string]struct{}{}
	for key := range m {
		out[key] = struct{}{}
	}
	return out
}

func funcKey(rel, name string) string {
	return rel + "#" + name
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range in {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
