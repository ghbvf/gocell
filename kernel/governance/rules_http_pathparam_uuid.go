package governance

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const CodeContractHealthPathParamUUID = "CH-05" // SeverityError

// CheckHTTPPathParamUUID enforces CH-05: for every contract with
// pathParams.{name}.format == "uuid", the corresponding handler must call
// httputil.ParseUUIDPathParam(w, r, "{name}") for that parameter.
//
// Contracts without a matching in-repo handler are silently skipped.
//
// CH-05 reuses the same parsedHandlerFile cache and contractToFuncs mapping
// from CH-04 (rules_http_response_alignment.go) to narrow the walk to the
// specific handler function linked to each contract via auth.Mount. When no
// auth.Mount correlation is found the rule emits a SeverityError finding
// (fail-closed) rather than falling back to whole-file scanning.
func (v *Validator) CheckHTTPPathParamUUID(contracts []*metadata.ContractMeta, projectRoot string) []ValidationResult {
	// Share the same parse cache across all contracts in one call; avoids
	// re-parsing the same handler.go for every contract it serves.
	cache := map[string]*parsedHandlerFile{}
	var results []ValidationResult
	for _, c := range contracts {
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.checkPathParamUUIDForContract(c, projectRoot, cache)...)
	}
	return results
}

func (v *Validator) checkPathParamUUIDForContract(c *metadata.ContractMeta, projectRoot string, cache map[string]*parsedHandlerFile) []ValidationResult {
	uuidParams := collectUUIDPathParams(c)
	if len(uuidParams) == 0 {
		return nil
	}

	handlerFile := findHandlerFile(v.project, c.ID, projectRoot)
	if handlerFile == "" {
		return nil
	}

	ph, err := parseHandlerFile(handlerFile, cache)
	if err != nil {
		return nil
	}

	fnName, ok := ph.contractToFuncs[c.ID]
	if !ok {
		return []ValidationResult{v.newResult(
			CodeContractHealthPathParamUUID, SeverityError, IssueRequired,
			c.File, "endpoints.http.path",
			fmt.Sprintf("CH-05: contract %s with `pathParams.{name}.format: uuid` — auth.Mount correlation failed; cannot verify ParseUUIDPathParam call within handler function. Required: handler must use `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})` pattern.", c.ID),
		)}
	}

	body, ok := ph.funcBodies[fnName]
	if !ok {
		return []ValidationResult{v.newResult(
			CodeContractHealthPathParamUUID, SeverityError, IssueRequired,
			c.File, "endpoints.http.path",
			fmt.Sprintf("CH-05: contract %s with `pathParams.{name}.format: uuid` — auth.Mount correlation failed; cannot verify ParseUUIDPathParam call within handler function. Required: handler must use `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})` pattern.", c.ID),
		)}
	}

	parsed := collectParsedUUIDParamNamesFromAST(body)
	return buildPathParamFindings(v, c, uuidParams, parsed)
}

// buildPathParamFindings compares required UUID params vs parsed call sites.
func buildPathParamFindings(v *Validator, c *metadata.ContractMeta, uuidParams []string, parsed map[string]struct{}) []ValidationResult {
	var results []ValidationResult
	for _, paramName := range uuidParams {
		if _, ok := parsed[paramName]; !ok {
			results = append(results, v.newResult(
				CodeContractHealthPathParamUUID, SeverityError, IssueRequired,
				c.File, fmt.Sprintf("endpoints.http.pathParams.%s", paramName),
				fmt.Sprintf("%s: pathParam %q has format:uuid but handler does not call httputil.ParseUUIDPathParam(w, r, %q)", c.ID, paramName, paramName),
			))
		}
	}
	return results
}

// collectUUIDPathParams returns a sorted slice of path-param names that have
// format == "uuid" in the contract.
func collectUUIDPathParams(c *metadata.ContractMeta) []string {
	if c.Endpoints.HTTP == nil {
		return nil
	}
	var names []string
	for name, schema := range c.Endpoints.HTTP.PathParams {
		if schema.Format == "uuid" {
			names = append(names, name)
		}
	}
	// Sort for deterministic finding order across Go map iteration.
	sort.Strings(names)
	return names
}

// collectParsedUUIDParamNamesFromNode walks a single AST node (typically a
// function body) and returns the set of parameter names passed to
// httputil.ParseUUIDPathParam(w, r, "<name>") calls within it.
func collectParsedUUIDParamNamesFromAST(node ast.Node) map[string]struct{} {
	found := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		collectParseUUIDCallName(call, found)
		return true
	})
	return found
}

// collectParseUUIDCallName inspects a single call expression and, if it is
// httputil.ParseUUIDPathParam(w, r, "<name>"), adds "<name>" to found.
func collectParseUUIDCallName(call *ast.CallExpr, found map[string]struct{}) {
	if !isParseUUIDPathParamCall(call) {
		return
	}
	if len(call.Args) < 3 {
		return
	}
	lit, ok := call.Args[2].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	name := lit.Value
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = name[1 : len(name)-1]
	}
	found[name] = struct{}{}
}

// isParseUUIDPathParamCall returns true when call is
// httputil.ParseUUIDPathParam(...).
func isParseUUIDPathParamCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "httputil" && sel.Sel.Name == "ParseUUIDPathParam"
}
