package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

		// On pagination endpoints (cursor + limit both declared, with any
		// number of additional filter params allowed), cursor/limit length
		// + range checks are single-sourced via httputil.ParsePageParams
		// (query.MaxCursorTokenBytes / query.MaxPageSize). Per-param skip
		// covers `cursor + limit + status` style filters without false
		// positives — the legacy `len(qp) == 2` exact-match would have
		// flagged the filter case as non-pagination and required redundant
		// length checks on cursor/limit.
		qp := contract.Endpoints.HTTP.QueryParams
		isPagination := false
		if qp != nil {
			_, hasCursor := qp["cursor"]
			_, hasLimit := qp["limit"]
			isPagination = hasCursor && hasLimit
		}

		for name, p := range contract.Endpoints.HTTP.PathParams {
			if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
				expects = append(expects, expectation{contract.ID, name, true, p.MinLength, p.MaxLength, genPath})
			}
		}
		for name, p := range qp {
			if isPagination && (name == "cursor" || name == "limit") {
				continue
			}
			if p.Type == "string" && (p.MinLength != nil || p.MaxLength != nil) {
				expects = append(expects, expectation{contract.ID, name, false, p.MinLength, p.MaxLength, genPath})
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
	if len(text) == 0 || paramName == "" {
		return false
	}
	return strings.Contains(text, paramName+`: value too short`) ||
		strings.Contains(text, paramName+`: value too long`)
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
	findTarget := pathParamTargetFinder(paramName)
	if !isPath {
		findTarget = queryParamTargetFinder(paramName)
	}
	return scanParamLengthCheck(handle.Body, findTarget, requireMin, requireMax)
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

// targetFinder returns the AST expression `len(...)` should compare against
// for the given block, or (nil, false) if the block does not own the param.
type targetFinder func(block *ast.BlockStmt) (ast.Expr, bool)

// pathParamTargetFinder binds paramName to a finder that locates path-param
// blocks (`v := r.PathValue("paramName")` or the `httputil.Parse*PathParam`
// helper form) and returns the local ident as the len-target.
func pathParamTargetFinder(paramName string) targetFinder {
	return func(block *ast.BlockStmt) (ast.Expr, bool) {
		lhs, found := pathValueLHS(block, paramName)
		if !found {
			return nil, false
		}
		return &ast.Ident{Name: lhs}, true
	}
}

// queryParamTargetFinder binds paramName to a finder that locates the
// `req.<GoName> = r.URL.Query().Get("paramName")` assignment and returns
// `req.<GoName>` as the len-target.
func queryParamTargetFinder(paramName string) targetFinder {
	return func(block *ast.BlockStmt) (ast.Expr, bool) {
		goName, found := queryParamAssignment(block, paramName)
		if !found {
			return nil, false
		}
		return &ast.SelectorExpr{X: &ast.Ident{Name: "req"}, Sel: &ast.Ident{Name: goName}}, true
	}
}

// scanParamLengthCheck walks body for the BlockStmt where findTarget
// resolves and asserts the matching min/max len IfStmts are bound to that
// same block.
func scanParamLengthCheck(body *ast.BlockStmt, findTarget targetFinder, requireMin, requireMax bool) bool {
	matched := false
	ast.Inspect(body, func(n ast.Node) bool {
		if matched {
			return false
		}
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		target, found := findTarget(block)
		if !found {
			return true
		}
		if !blockSatisfiesLenChecks(block, target, requireMin, requireMax) {
			// return false stops descent into this block's children; sibling
			// blocks continue via the parent traversal.
			return false
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

// TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_PositiveFixture asserts the
// param-scoped scanner correctly ACCEPTS a compliant handler that exercises
// every supported param-shape (two non-UUID path params, one UUID helper
// path param, one query param at function-body level).
func TestHANDLER_PATH_QUERY_LENGTH_VALIDATION_01_PositiveFixture(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	path := filepath.Join(archDir, "testdata", "path_query_length_validation_fixtures", "compliant", "handler_gen.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}

	cases := []struct {
		paramName  string
		isPath     bool
		requireMin bool
		requireMax bool
	}{
		{"id", true, true, true},
		{"cmdId", true, true, true},
		// Token via httputil.ParseUUIDPathParam helper: the helper performs
		// length+format validation internally, so the contract declares no
		// min/max and the scanner is not invoked for this param. The fixture
		// still includes the helper call site to exercise callMatchesPathHelper
		// in pathValueLHS — verified indirectly via the lhs-resolution path.
		{"actorId", false, true, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.paramName, func(t *testing.T) {
			t.Parallel()
			if !scanHandlerLengthCheck(f, tc.paramName, tc.isPath, tc.requireMin, tc.requireMax) {
				t.Errorf("HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 positive fixture compliant param %q "+
					"(isPath=%v): scanner FALSE-NEGATIVE; required min/max checks are present",
					tc.paramName, tc.isPath)
			}
		})
	}

	// pathValueLHS must also resolve the UUID helper form even though the
	// gate does not enforce length checks for UUID params (helper handles
	// that internally). Verified by directly invoking pathParamTargetFinder
	// against the handler body's UUID block.
	t.Run("uuid_helper_lhs_resolution", func(t *testing.T) {
		t.Parallel()
		fn := findHandleFunc(f)
		if fn == nil || fn.Body == nil {
			t.Fatalf("handle func not found in compliant fixture")
		}
		find := pathParamTargetFinder("token")
		var resolved bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			block, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			if _, ok := find(block); ok {
				resolved = true
				return false
			}
			return true
		})
		if !resolved {
			t.Errorf("pathValueLHS did not resolve UUID helper form `httputil.ParseUUIDPathParam(... \"token\")`")
		}
	})
}
