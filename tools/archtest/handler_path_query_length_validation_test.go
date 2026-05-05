package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 ensures that whenever a contract.yaml
// declares minLength/maxLength on a path or query parameter, the generated
// handler enforces it at runtime. This guards the regression surfaced after
// PR-V1-CODEGEN-FULL-MIGRATION-FU B4 had initially deleted these inline checks
// when migrating body validation to santhosh-tekuri/jsonschema/v6.
//
// Body validation is delegated to the schema validator and is NOT scanned here;
// only path/query string-length constraints are checked because schemavalidate
// only consumes the request body schema.
//
// Error message contract: handlers must use the generic "{name}: invalid"
// format (no "value too short" / "value too long") to avoid length oracle
// attacks (cf. F-SEC-001 in docs/reviews/202605051730-PR376/).
//
// Scope: uses metadata.NewParser (LoadProject equivalent) so all contracts
// including examples/{iotdevice,todoorder}/contracts are scanned — not just
// the top-level contracts/ directory.
//
// Behavior assertion: AST-scans handler_gen.go for BinaryExpr nodes of the form
// `len(v) < N` / `len(req.Field) < N` / `len(v) > N` / `len(req.Field) > N`
// which are the canonical patterns the template emits for minLength/maxLength.
// This replaces the prior strings.Contains scan which was a text-level heuristic.
func TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	type expectation struct {
		contractID string
		paramName  string
		isPath     bool
		minLen     *int
		maxLen     *int
		generated  string
	}

	var expects []expectation
	for _, contract := range project.Contracts {
		if !contract.Codegen || contract.Kind != "http" || contract.Endpoints.HTTP == nil {
			continue
		}
		gen := contractIDToExpectedPkgPath(contract.ID)
		genPath := filepath.Join(root, gen, "handler_gen.go")

		// Pagination endpoints (queryParams = {cursor, limit}) delegate cursor /
		// limit length+range checks to httputil.ParsePageParams (single source of
		// truth: query.MaxCursorTokenBytes / query.MaxPageSize). contract.yaml
		// length constraints on cursor/limit are documentation-only here.
		isPagination := false
		qp := contract.Endpoints.HTTP.QueryParams
		if qp != nil {
			_, hasCursor := qp["cursor"]
			_, hasLimit := qp["limit"]
			isPagination = hasCursor && hasLimit && len(qp) == 2
		}

		for name, p := range contract.Endpoints.HTTP.PathParams {
			if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
				expects = append(expects, expectation{contract.ID, name, true, p.MinLength, p.MaxLength, genPath})
			}
		}
		if !isPagination {
			for name, p := range contract.Endpoints.HTTP.QueryParams {
				if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
					expects = append(expects, expectation{contract.ID, name, false, p.MinLength, p.MaxLength, genPath})
				}
			}
		}
	}

	if len(expects) == 0 {
		t.Fatal("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01: no contract with " +
			"path/query length constraint found — survey expected contracts " +
			"with pathParams/queryParams minLength/maxLength; check survey logic")
	}

	t.Logf("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01: checking %d param length constraints "+
		"across all contracts (including examples)", len(expects))

	for _, e := range expects {
		// Oracle guard: text-level check for banned length-exposing messages.
		body, err := os.ReadFile(e.generated)
		if err != nil {
			t.Errorf("%s param %q: cannot read generated handler %s: %v", e.contractID, e.paramName, e.generated, err)
			continue
		}

		// Error message contract: "{name}: value too short" / "value too long" must
		// NOT appear to prevent length oracle attacks.
		text := string(body)
		if containsOracleMessage(text, e.paramName) {
			t.Errorf("%s param %q: handler exposes length oracle in error message; use \"{name}: invalid\" form",
				e.contractID, e.paramName)
		}

		// AST assertion: verify the handler actually enforces the constraint via
		// a BinaryExpr `len(x) < N` or `len(x) > N`.
		if e.minLen != nil || e.maxLen != nil {
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, e.generated, nil, 0)
			if parseErr != nil {
				t.Errorf("%s param %q: cannot parse generated handler %s: %v",
					e.contractID, e.paramName, e.generated, parseErr)
				continue
			}
			if !handlerHasLengthCheck(f, e.paramName) {
				t.Errorf("%s param %q: contract declares min/maxLength but handler %s "+
					"lacks a `len(%s)` length-check BinaryExpr",
					e.contractID, e.paramName, e.generated, e.paramName)
			}
		}
	}
}

// containsOracleMessage returns true if the handler text contains a length
// oracle error message for the given param name.
func containsOracleMessage(text, paramName string) bool {
	// The old/banned patterns that expose specific constraint values.
	if len(text) == 0 || paramName == "" {
		return false
	}
	// Check for the banned "value too short" / "value too long" patterns.
	return contains(text, paramName+`: value too short`) ||
		contains(text, paramName+`: value too long`)
}

// contains is a thin wrapper to make containsOracleMessage readable without
// importing strings (already imported via metadata).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		findSubstring(s, sub))
}

// findSubstring searches for sub in s using a simple scan.
func findSubstring(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// handlerHasLengthCheck reports whether the parsed handler file contains an AST
// BinaryExpr of the form `len(<expr-containing-paramName>) < N` or `> N`.
// The paramName must appear somewhere in the len() argument (e.g. as the
// identifier `v`, `req.ParamGoName`, or in a string literal used to extract it).
// We use a conservative heuristic: scan for BinaryExpr where one side is a
// CallExpr to `len` and the other side is a BasicLit integer, AND the function
// body contains the param name as a string identifier or string literal.
//
// Strategy: two-phase scan.
//  1. Collect all len(x) < N / len(x) > N BinaryExprs in the file.
//  2. For each such expr, check if the len() argument or nearby context
//     references the paramName.
func handlerHasLengthCheck(f *ast.File, paramName string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		op := bin.Op.String()
		if op != "<" && op != ">" {
			return true
		}

		// One side should be a len() call, the other a numeric literal.
		lenCall := extractLenCallAndLiteral(bin)
		if lenCall == nil {
			return true
		}

		// Check if the len() argument references the param name.
		// The template uses either:
		//   len(v) < N  (where v was set from r.PathValue("paramName") or Query().Get("paramName"))
		//   len(req.GoName) < N  (not currently emitted but covered for future)
		// We accept any len() call in a block that also contains "paramName" as
		// a string literal (r.PathValue / Query.Get argument).
		// Simpler: check if the file text near this node mentions paramName.
		// Since AST nodes don't carry text, we do a wider check:
		// look for any ident or string literal in the len() arg referencing paramName.
		if lenArgContainsParamRef(lenCall, paramName) {
			found = true
		}
		return true
	})

	if found {
		return true
	}

	// Fallback: scan for the quoted param name string literal anywhere in the
	// file. The template emits `r.PathValue("paramName")` and
	// `r.URL.Query().Get("paramName")` adjacent to the len() check.
	// If the file contains both a len() BinaryExpr AND the quoted param name,
	// we accept it as a match. This handles the common template pattern where
	// `v` is a local variable set from the path/query value.
	hasLenCheck := false
	hasParamRef := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			op := node.Op.String()
			if op == "<" || op == ">" {
				if extractLenCallAndLiteral(node) != nil {
					hasLenCheck = true
				}
			}
		case *ast.BasicLit:
			if node.Kind == token.STRING && node.Value == `"`+paramName+`"` {
				hasParamRef = true
			}
		}
		return true
	})
	return hasLenCheck && hasParamRef
}

// extractLenCallAndLiteral returns the len() CallExpr from a BinaryExpr where
// one side is len(...) and the other side is a BasicLit integer.
// Returns nil if neither side matches the pattern.
func extractLenCallAndLiteral(bin *ast.BinaryExpr) *ast.CallExpr {
	if call, ok := bin.X.(*ast.CallExpr); ok {
		if lit, ok := bin.Y.(*ast.BasicLit); ok && lit.Kind == token.INT {
			if isLenIdent(call.Fun) {
				return call
			}
		}
	}
	if call, ok := bin.Y.(*ast.CallExpr); ok {
		if lit, ok := bin.X.(*ast.BasicLit); ok && lit.Kind == token.INT {
			if isLenIdent(call.Fun) {
				return call
			}
		}
	}
	return nil
}

// isLenIdent returns true if expr is the identifier `len`.
func isLenIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "len"
}

// lenArgContainsParamRef checks whether the argument to a len() call directly
// references the param name (as a SelectorExpr field or Ident).
// The template emits `len(v)` where `v` is a short local variable, so this
// check alone is insufficient. Returns true only for direct references like
// `len(req.ParamGoName)`.
func lenArgContainsParamRef(call *ast.CallExpr, paramName string) bool {
	if len(call.Args) != 1 {
		return false
	}
	arg := call.Args[0]
	// Check for SelectorExpr: req.ParamGoName (camelCase)
	// ParamGoName is PascalCase; paramName is the raw YAML key.
	// We compare case-insensitively on the field name.
	if sel, ok := arg.(*ast.SelectorExpr); ok {
		if eqFold(sel.Sel.Name, paramName) {
			return true
		}
	}
	return false
}

// TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_NegativeFixtures asserts that
// each negative fixture below is correctly REJECTED by the param-scoped
// scanner. The legacy file-level fallback (`hasLenCheck && hasParamRef`)
// FALSE-PASSes them; this Wave 1 RED test FAILS pre-refactor and PASSes
// after the GREEN param-scoped helper lands.
func TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_NegativeFixtures(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)

	cases := []struct {
		name      string
		fixture   string
		paramName string
	}{
		// Two path params: "id" has full min/max checks, "cmdId" has none.
		// Legacy fallback returns true for "cmdId" because the file-level
		// hasLenCheck (id's len(v) compares) AND hasParamRef ("cmdId" string
		// literal in r.PathValue) flags collide independently.
		{"two_path_params_one_missing", "two_path_params_one_missing", "cmdId"},

		// String literal carrier: "fakeparam" appears in r.PathValue but the
		// only len() compare in the file is `len(body) > 1024` for body bytes.
		// Legacy fallback FALSE-PASSes because both flags are set globally.
		{"string_literal_only", "string_literal_only", "fakeparam"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(archDir, "testdata", "path_query_length_validation_fixtures", tc.fixture, "handler_gen.go")
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse fixture %s: %v", path, err)
			}
			if handlerHasLengthCheck(f, tc.paramName) {
				t.Errorf("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 negative fixture %q: "+
					"legacy file-level scanner FALSE-PASSes for param %q; param-scoped GREEN "+
					"refactor required (block-binding of len() compare to PathValue/Query.Get)",
					tc.fixture, tc.paramName)
			}
		})
	}
}

// eqFold compares two strings case-insensitively.
func eqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
