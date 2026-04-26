package governance

import (
	"fmt"
	"go/ast"
	"go/token"
	"log/slog"
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
// auth.Mount correlation is found the rule falls back to whole-file scanning
// and emits a SeverityWarn note.
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
		slog.Debug("CH-05: no handler file found for contract, skipping",
			slog.String("contract", c.ID))
		return nil
	}

	ph, err := parseHandlerFile(handlerFile, cache)
	if err != nil {
		slog.Debug("CH-05: failed to parse handler AST",
			slog.String("contract", c.ID),
			slog.String("file", handlerFile),
			slog.Any("error", err))
		return nil
	}

	// Narrow to the specific handler function when auth.Mount correlation is
	// available; fall back to whole-file scan with a Warn note otherwise.
	var searchNode ast.Node
	narrowed := false
	if fnName, ok := ph.contractToFuncs[c.ID]; ok {
		if body, ok := ph.funcBodies[fnName]; ok {
			searchNode = body
			narrowed = true
		}
	}

	var results []ValidationResult
	if !narrowed {
		// No auth.Mount correlation — walk entire file. Emit a Warn so that
		// developers know the check is less precise than usual.
		slog.Warn("CH-05: no auth.Mount correlation found, falling back to file-wide scan",
			slog.String("contract", c.ID),
			slog.String("file", handlerFile))
		// Collect from allCodes equivalent: scan all function bodies.
		parsed := collectParsedUUIDParamNamesFromNode(ph.funcBodies)
		return buildPathParamFindings(v, c, uuidParams, parsed)
	}

	parsed := collectParsedUUIDParamNamesFromAST(searchNode)
	return append(results, buildPathParamFindings(v, c, uuidParams, parsed)...)
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

// collectParsedUUIDParamNamesFromNode walks all function bodies in the map
// (used for the fallback whole-file scan path).
func collectParsedUUIDParamNamesFromNode(funcBodies map[string]ast.Node) map[string]struct{} {
	found := make(map[string]struct{})
	for _, body := range funcBodies {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			collectParseUUIDCallName(call, found)
			return true
		})
	}
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
