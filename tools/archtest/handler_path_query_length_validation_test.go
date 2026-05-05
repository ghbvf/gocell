package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
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
// Behavior assertion: AST scan with **param-scoped block-binding**. For each
// path/query param declaring minLength/maxLength, the scanner finds the
// scope where that specific param is read (PathValue("name") in a path-param
// block, or req.X = r.URL.Query().Get("name") at function-body level for
// query params) and asserts a `len(target) < / > N` IfStmt is bound to the
// same scope. Cross-param FALSE-PASS via global len-check + global string
// literal flags (the legacy file-level fallback) is not allowed.
//
// ref: docs/reviews/202605051730-PR376/ F6 finding
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

		// AST assertion: param-scoped block-binding. The scanner locates the
		// scope where this specific param is read and asserts the matching
		// length-compare IfStmt is bound to that scope.
		if e.minLen != nil || e.maxLen != nil {
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, e.generated, nil, 0)
			if parseErr != nil {
				t.Errorf("%s param %q: cannot parse generated handler %s: %v",
					e.contractID, e.paramName, e.generated, parseErr)
				continue
			}
			requireMin := e.minLen != nil
			requireMax := e.maxLen != nil
			if !scanHandlerLengthCheck(f, e.paramName, e.isPath, requireMin, requireMax) {
				t.Errorf("%s param %q (isPath=%v, requireMin=%v, requireMax=%v): contract "+
					"declares min/maxLength but handler %s lacks the matching param-scoped "+
					"`len(...) < / > N` IfStmt(s)",
					e.contractID, e.paramName, e.isPath, requireMin, requireMax, e.generated)
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

// scanHandlerLengthCheck reports whether the parsed handler file declares the
// expected param-scoped length checks for paramName. For path params the
// scanner locates the *ast.BlockStmt that contains
// `r.PathValue("<paramName>")` and asserts a matching `len(<lhs>) < / > N`
// IfStmt is bound to the same block. For query params it locates
// `req.<GoName> = r.URL.Query().Get("<paramName>")` and asserts the
// matching `len(req.<GoName>) < / > N` IfStmt sits in the same enclosing
// block (template emits the IfStmt at function-body level immediately after
// the assignment).
//
// requireMin / requireMax independently gate min/max IfStmt assertions —
// extra IfStmts are tolerated.
func scanHandlerLengthCheck(f *ast.File, paramName string, isPath, requireMin, requireMax bool) bool {
	handle := findHandleFunc(f)
	if handle == nil || handle.Body == nil {
		return false
	}
	if isPath {
		return scanPathParamLengthCheck(handle.Body, paramName, requireMin, requireMax)
	}
	return scanQueryParamLengthCheck(handle.Body, paramName, requireMin, requireMax)
}

// findHandleFunc returns the *Handler.handle method declaration in f, or nil
// if absent.
func findHandleFunc(f *ast.File) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "handle" {
			continue
		}
		if receiverTypeName(fn.Recv.List[0].Type) == "Handler" {
			return fn
		}
	}
	return nil
}

// scanPathParamLengthCheck walks the handle function body for nested
// BlockStmts containing `<lhs> := r.PathValue("paramName")` (or `:=` with
// helper like ParseUUIDPathParam) and asserts the matching len(lhs)
// IfStmts live in that same block.
func scanPathParamLengthCheck(body *ast.BlockStmt, paramName string, requireMin, requireMax bool) bool {
	matched := false
	ast.Inspect(body, func(n ast.Node) bool {
		if matched {
			return false
		}
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		lhs, found := pathValueLHS(block, paramName)
		if !found {
			return true
		}
		target := &ast.Ident{Name: lhs}
		if !blockSatisfiesLenChecks(block, target, requireMin, requireMax) {
			return false // walk continues but this block does not satisfy
		}
		matched = true
		return false
	})
	return matched
}

// pathValueLHS returns the local variable name (e.g. "v") assigned from
// r.PathValue("paramName") within the given block, plus a found flag.
func pathValueLHS(block *ast.BlockStmt, paramName string) (string, bool) {
	for _, stmt := range block.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) == 0 {
			continue
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}
		// match r.PathValue("paramName") on RHS (single-return form: v := r.PathValue("x"))
		if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && callMatchesPathValue(call, paramName) {
			return ident.Name, true
		}
	}
	// Also accept the two-return helper form:
	//   v, ok := httputil.ParseUUIDPathParam(w, r, "name")
	for _, stmt := range block.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) < 1 || len(assign.Rhs) == 0 {
			continue
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}
		if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && callMatchesPathHelper(call, paramName) {
			return ident.Name, true
		}
	}
	return "", false
}

// callMatchesPathValue returns true for r.PathValue("paramName").
func callMatchesPathValue(call *ast.CallExpr, paramName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "PathValue" {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "r" {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	return val == paramName
}

// callMatchesPathHelper returns true for httputil.Parse...PathParam(w, r, "name").
func callMatchesPathHelper(call *ast.CallExpr, paramName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "httputil" {
		return false
	}
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		if val == paramName {
			return true
		}
	}
	return false
}

// scanQueryParamLengthCheck walks the body for the assignment
// `req.<GoName> = r.URL.Query().Get("paramName")` and asserts the matching
// len(req.<GoName>) IfStmts sit in the same enclosing block.
func scanQueryParamLengthCheck(body *ast.BlockStmt, paramName string, requireMin, requireMax bool) bool {
	matched := false
	ast.Inspect(body, func(n ast.Node) bool {
		if matched {
			return false
		}
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		goName, found := queryParamAssignment(block, paramName)
		if !found {
			return true
		}
		target := &ast.SelectorExpr{X: &ast.Ident{Name: "req"}, Sel: &ast.Ident{Name: goName}}
		if !blockSatisfiesLenChecks(block, target, requireMin, requireMax) {
			return false
		}
		matched = true
		return false
	})
	return matched
}

// queryParamAssignment returns the goName field assigned from
// `r.URL.Query().Get("paramName")` within the block.
func queryParamAssignment(block *ast.BlockStmt, paramName string) (string, bool) {
	for _, stmt := range block.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			continue
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			continue
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "req" {
			continue
		}
		if !rhsMatchesQueryGet(assign.Rhs[0], paramName) {
			continue
		}
		return sel.Sel.Name, true
	}
	return "", false
}

// rhsMatchesQueryGet returns true for `r.URL.Query().Get("paramName")`.
func rhsMatchesQueryGet(expr ast.Expr, paramName string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	getSel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || getSel.Sel.Name != "Get" {
		return false
	}
	queryCall, ok := getSel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	querySel, ok := queryCall.Fun.(*ast.SelectorExpr)
	if !ok || querySel.Sel.Name != "Query" {
		return false
	}
	urlSel, ok := querySel.X.(*ast.SelectorExpr)
	if !ok || urlSel.Sel.Name != "URL" {
		return false
	}
	if x, ok := urlSel.X.(*ast.Ident); !ok || x.Name != "r" {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	return val == paramName
}

// blockSatisfiesLenChecks returns true if the block contains, immediately at
// statement level (inside any nested IfStmt at this depth), the requested
// `len(target) < N` and/or `len(target) > N` IfStmts.
func blockSatisfiesLenChecks(block *ast.BlockStmt, target ast.Expr, requireMin, requireMax bool) bool {
	gotMin, gotMax := false, false
	for _, stmt := range block.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		op, matches := ifMatchesLenCompare(ifStmt.Cond, target)
		if !matches {
			continue
		}
		switch op {
		case token.LSS:
			gotMin = true
		case token.GTR:
			gotMax = true
		}
	}
	if requireMin && !gotMin {
		return false
	}
	if requireMax && !gotMax {
		return false
	}
	return true
}

// ifMatchesLenCompare returns the op (<, >) if cond matches
// `len(target) < N`, `len(target) > N`, or a logical AND chain whose last
// term is one of those (the template emits `req.X != "" && len(req.X) < N`).
// Returns ok=false otherwise.
func ifMatchesLenCompare(cond ast.Expr, target ast.Expr) (token.Token, bool) {
	if be, ok := cond.(*ast.BinaryExpr); ok {
		if be.Op == token.LAND {
			// recurse into right-hand side (template form: prefix != "" && len() < N)
			return ifMatchesLenCompare(be.Y, target)
		}
		if be.Op != token.LSS && be.Op != token.GTR {
			return token.ILLEGAL, false
		}
		// One side is len(target), the other is INT literal.
		if isLenCallOnTarget(be.X, target) && isIntLit(be.Y) {
			return be.Op, true
		}
		// Mirror form (N < len(x)) is unusual but accepted symmetrically.
		if isLenCallOnTarget(be.Y, target) && isIntLit(be.X) {
			// flip op for canonical direction
			if be.Op == token.LSS {
				return token.GTR, true
			}
			return token.LSS, true
		}
	}
	return token.ILLEGAL, false
}

// isLenCallOnTarget returns true for `len(<expr-equal-to-target>)`.
func isLenCallOnTarget(expr ast.Expr, target ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "len" {
		return false
	}
	return exprEqual(call.Args[0], target)
}

// exprEqual is a structural equality for the small subset of AST expressions
// the templates emit (Ident, SelectorExpr{X:Ident, Sel:Ident}).
func exprEqual(a, b ast.Expr) bool {
	switch x := a.(type) {
	case *ast.Ident:
		y, ok := b.(*ast.Ident)
		return ok && x.Name == y.Name
	case *ast.SelectorExpr:
		y, ok := b.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		xi, _ := x.X.(*ast.Ident)
		yi, _ := y.X.(*ast.Ident)
		if xi == nil || yi == nil || xi.Name != yi.Name {
			return false
		}
		return x.Sel.Name == y.Sel.Name
	}
	return false
}

// isIntLit reports whether expr is a *ast.BasicLit of kind INT.
func isIntLit(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	return ok && lit.Kind == token.INT
}

// TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_NegativeFixtures asserts the
// param-scoped scanner correctly REJECTS each negative fixture. Legacy
// file-level fallback FALSE-PASSed these (cross-param flag collision).
func TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_NegativeFixtures(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)

	cases := []struct {
		name       string
		fixture    string
		paramName  string
		isPath     bool
		requireMin bool
		requireMax bool
	}{
		// Two path params: "id" has full min/max checks, "cmdId" has none.
		// Legacy fallback FALSE-PASSed for "cmdId" because file-level flags
		// collided across params.
		{"two_path_params_one_missing", "two_path_params_one_missing", "cmdId", true, true, true},

		// String literal carrier: "fakeparam" appears in r.PathValue but the
		// only len() compare in the file is `len(body) > 1024` for body
		// bytes. Param-scoped scanner finds the PathValue block but that
		// block has no len(v) IfStmt — correctly REJECTed.
		{"string_literal_only", "string_literal_only", "fakeparam", true, true, true},

		// Query param with min check only — missing > N IfStmt.
		{"query_missing_max", "query_missing_max", "actorId", false, true, true},
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
			if scanHandlerLengthCheck(f, tc.paramName, tc.isPath, tc.requireMin, tc.requireMax) {
				t.Errorf("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 negative fixture %q param %q "+
					"(isPath=%v): scanner FALSE-PASSes; param-scoped block-binding required",
					tc.fixture, tc.paramName, tc.isPath)
			}
		})
	}
}
