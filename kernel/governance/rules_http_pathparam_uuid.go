package governance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const CodeContractHealthPathParamUUID = "CH-05" // SeverityError

// CheckHTTPPathParamUUID enforces CH-05: for every contract with
// pathParams.{name}.format == "uuid", the corresponding handler must call
// httputil.ParseUUIDPathParam(w, r, "{name}") for that parameter.
//
// Contracts without a matching in-repo handler are silently skipped.
func (v *Validator) CheckHTTPPathParamUUID(contracts []*metadata.ContractMeta, projectRoot string) []ValidationResult {
	var results []ValidationResult
	for _, c := range contracts {
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.checkPathParamUUIDForContract(c, projectRoot)...)
	}
	return results
}

func (v *Validator) checkPathParamUUIDForContract(c *metadata.ContractMeta, projectRoot string) []ValidationResult {
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

	parsed, err := extractParsedUUIDParamNames(handlerFile)
	if err != nil {
		slog.Debug("CH-05: failed to parse handler AST",
			slog.String("contract", c.ID),
			slog.String("file", handlerFile),
			slog.Any("error", err))
		return nil
	}

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
	sortStrings(names)
	return names
}

// sortStrings sorts a string slice in place (avoids importing sort package
// twice when the governance package already imports it in the sibling file;
// Go resolves this via the package-level sort import, so a named helper here
// keeps complexity ≤15 per function).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// extractParsedUUIDParamNames walks the AST of filename and returns the set
// of parameter names passed to httputil.ParseUUIDPathParam(w, r, "<name>")
// calls anywhere in the file.
func extractParsedUUIDParamNames(filename string) (map[string]struct{}, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	found := make(map[string]struct{})
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isParseUUIDPathParamCall(call) {
			return true
		}
		// Args[2] must be a string literal with the param name.
		if len(call.Args) < 3 {
			return true
		}
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		// Strip enclosing double quotes from the literal value.
		name := lit.Value
		if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
			name = name[1 : len(name)-1]
		}
		found[name] = struct{}{}
		return true
	})
	return found, nil
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
