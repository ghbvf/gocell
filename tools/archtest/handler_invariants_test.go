package archtest

// handler_invariants_test.go consolidates handler-theme invariants:
//   - INVARIANT: HANDLER-NO-INLINE-LIMIT-PARSE-01
//   - INVARIANT: HANDLER-NO-SCHEMA-FOR-NOBODY-01
//   - INVARIANT: HANDLER-PATH-QUERY-LENGTH-VALIDATION-01
//   - INVARIANT: HANDLER-POLICY-REQUIRED-01
//   - INVARIANT: HANDLER-VALIDATOR-FAIL-FAST-01

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	handlerInlineLimitParseRule  = "HANDLER-NO-INLINE-LIMIT-PARSE-01"
	handlerNoSchemaForNobodyRule = "HANDLER-NO-SCHEMA-FOR-NOBODY-01"
	handlerPolicyRequiredRule    = "HANDLER-POLICY-REQUIRED-01"
	handlerValidatorFailFastRule = "HANDLER-VALIDATOR-FAIL-FAST-01"
)

// inlineLimitParseViolation is one (file, function, line) coordinate where
// generated handler code parses a "limit" query param via strconv.ParseInt
// instead of httputil.ParsePageParams.
type inlineLimitParseViolation struct {
	File string
	Func string
	Line int
}

// methodsWithBody lists HTTP methods that may legitimately read a request body.
// Any method outside this set is treated as no-body for this gate.
var methodsWithBody = map[string]bool{
	"POST":  true,
	"PUT":   true,
	"PATCH": true,
}

// handlerPolicyPublicExemptPkgs lists the import alias prefixes whose generated
// NewHandler is single-arg (Public=true endpoint) or whose route protection is
// provided by contract.Clients (RequireCallerCell guard). After the B2 fix:
//   - "registercontract" — Public:true, NewHandler(svc Service) single-arg
//   - "internallistcontract" — /internal/v1/ path, Clients=["devicecell"],
//     auth.Mount auto-injects RequireCallerCell; no per-route Policy needed
var handlerPolicyPublicExemptPkgs = []string{
	"registercontract",     // http.device.register.v1 — Public:true
	"internallistcontract", // http.internal.devicecommands.list.v1 — /internal/v1/, Clients guard
}

// INVARIANT: HANDLER-NO-INLINE-LIMIT-PARSE-01
//
// handler_inline_limit_parse_test.go — HANDLER-NO-INLINE-LIMIT-PARSE-01.
//
// PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE F4 absorbs PR#376 F-COR-001: every
// paginated endpoint must route the cursor+limit pair through
// pkg/httputil.ParsePageParams so the limit error envelope is uniform across
// the entire HTTP surface. This rule statically guards the generator against
// regressing to per-param inline limit parsing — any generated handler that
// emits a strconv.ParseInt call alongside a "limit" string literal in the
// same function body is flagged as a codegen drift.
//
// The check is intentionally narrow: it only inspects the generated
// handler_gen.go files (cells/* and examples/* hand-written code may legitimately
// parse their own limit query params). Generated handlers should always go
// through ParsePageParams when the contract.yaml declares cursor+limit, no
// matter how many additional filter params are present (the F4 relaxation in
// builder.detectPagination + handler.tmpl pagination branch).
func TestHandlerNoInlineLimitParse(t *testing.T) {
	root := findModuleRoot(t)

	httpRoot := filepath.Join(root, "generated", "contracts", "http")
	require.DirExists(t, httpRoot, "generated/contracts/http must exist; run `gocell generate contract --all`")
	handlers := walkHandlerGenFiles(t, httpRoot)
	require.NotEmpty(t, handlers, "no generated handler_gen.go files found under generated/contracts/http")

	var violations []inlineLimitParseViolation
	for _, path := range handlers {
		violations = append(violations, scanInlineLimitParse(t, path)...)
	}

	if !assert.Empty(t, violations, handlerInlineLimitParseRule+": generated handlers must route limit through httputil.ParsePageParams") {
		for _, v := range violations {
			t.Logf("%s: %s:%d in func %s", handlerInlineLimitParseRule, v.File, v.Line, v.Func)
		}
	}
}

// walkHandlerGenFiles collects every handler_gen.go under base, recursive.
// Used when filepath.Glob's ** glob is not supported by the platform/version.
func walkHandlerGenFiles(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && d.Name() == "handler_gen.go" {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestScanInlineLimitParse_DetectsViolation is the reverse / self-validation
// test: it writes a synthetic handler_gen.go that intentionally pairs
// strconv.ParseInt with a "limit" string literal in the same function body
// and asserts that scanInlineLimitParse flags it. Without this test, a future
// refactor that silently broke the AST walk (e.g. inverted the condition,
// dropped the BasicLit branch) would let real violations slip past the
// archtest while the positive test still passed (because real generated
// handlers always conform).
func TestScanInlineLimitParse_DetectsViolation(t *testing.T) {
	dir := t.TempDir()
	violatingPath := filepath.Join(dir, "handler_gen.go")
	body := `package fixture

import (
	"net/http"
	"strconv"
)

func handle(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = v
	}
}
`
	require.NoError(t, os.WriteFile(violatingPath, []byte(body), 0o600))

	violations := scanInlineLimitParse(t, violatingPath)
	require.Len(t, violations, 1, "scanner must detect the synthetic violation")
	require.Equal(t, "handle", violations[0].Func)
}

// scanInlineLimitParse parses a single handler_gen.go and returns one
// violation per top-level function whose body contains both a strconv.ParseInt
// call and a string literal "limit". The two-condition match keeps the rule
// from flagging legitimate generic int64 query param parsing (a body that
// contains strconv.ParseInt for some unrelated param "page" is fine).
func scanInlineLimitParse(t *testing.T, path string) []inlineLimitParseViolation {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Logf("scan %s: parse error %v (skipping)", path, err)
		return nil
	}

	var out []inlineLimitParseViolation
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		hasParseInt := false
		hasLimitLiteral := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// strconv.ParseInt — receiver is package selector.
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "strconv" && x.Sel.Name == "ParseInt" {
					hasParseInt = true
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && (x.Value == `"limit"` || strings.EqualFold(x.Value, `"limit"`)) {
					hasLimitLiteral = true
				}
			}
			return true
		})
		if hasParseInt && hasLimitLiteral {
			out = append(out, inlineLimitParseViolation{
				File: path,
				Func: fd.Name.Name,
				Line: fset.Position(fd.Pos()).Line,
			})
		}
	}
	return out
}

// INVARIANT: HANDLER-NO-SCHEMA-FOR-NOBODY-01
//
// HANDLER-NO-SCHEMA-FOR-NOBODY-01 — forward gate that no-body HTTP endpoints
// (GET / DELETE) must NOT embed requestSchemaJSON or wire a request validator.
//
// Builder fix in W4 F5 only embeds the schema when endpointSpec.HasBody is
// true (i.e. POST/PUT/PATCH with a declared request schema). This gate locks
// that invariant so future template/builder changes can't silently
// re-introduce the dead code.
//
// For every generated HTTP handler_gen.go in generated/contracts/http/**, if
// the corresponding contract.yaml declares a method other than POST / PUT /
// PATCH (i.e. GET / DELETE / HEAD / OPTIONS), the handler file must NOT
// contain `requestSchemaJSON` literal or `schemavalidate.NewValidator` call.
//
// ref: deepmap/oapi-codegen — request validator emitted only for operations
// declaring requestBody.
func TestHandlerNoSchemaForNobody01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "http" || !contract.Codegen {
			continue
		}
		if contract.Endpoints.HTTP == nil {
			continue
		}
		method := strings.ToUpper(contract.Endpoints.HTTP.Method)
		if methodsWithBody[method] {
			continue
		}
		// This is a GET / DELETE / HEAD / OPTIONS endpoint — handler must
		// not embed schema or wire validator.
		pkgPath := contractIDToExpectedPkgPath(contract.ID)
		handlerPath := filepath.Join(root, pkgPath, "handler_gen.go")
		body, err := os.ReadFile(handlerPath) //nolint:gosec // archtest scans repo paths it discovered itself
		if err != nil {
			// Some handler shapes (e.g. event-only contracts) have no handler_gen.go.
			continue
		}
		text := string(body)
		if handlerEmbedsSchemaLiteral(text) {
			t.Errorf("%s: %s (method %s) handler embeds requestSchemaJSON literal — "+
				"no-body endpoints must skip schema embed (rebuild with W4 F5 builder)",
				handlerNoSchemaForNobodyRule, contract.ID, method)
		}
		if handlerWiresSchemaValidator(text) {
			t.Errorf("%s: %s (method %s) handler wires schemavalidate.NewValidator — "+
				"no-body endpoints must not compile a request validator",
				handlerNoSchemaForNobodyRule, contract.ID, method)
		}
	}
}

// handlerEmbedsSchemaLiteral reports whether the handler source declares a
// real top-level var named `requestSchemaJSON`. Comment / string-constant
// occurrences of the bytes do not count.
func handlerEmbedsSchemaLiteral(text string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handler_gen.go", text, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == "requestSchemaJSON" {
					return true
				}
			}
		}
	}
	return false
}

// handlerWiresSchemaValidator reports whether the handler source contains an
// actual *ast.CallExpr to schemavalidate.NewValidator. Comment / string-literal
// occurrences do not count.
func handlerWiresSchemaValidator(text string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handler_gen.go", text, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if x.Name == "schemavalidate" && sel.Sel.Name == "NewValidator" {
			found = true
		}
		return true
	})
	return found
}

// TestHandlerNoSchemaForNobody01_NegativeFixture_StringLiteralOnly asserts the
// scanner does NOT flag a fixture that only contains "requestSchemaJSON" /
// "schemavalidate.NewValidator" in comments and string-constant values, with
// no real var/CallExpr. Legacy strings.Contains FALSE-POSITIVES; AST GREEN
// refactor must distinguish.
func TestHandlerNoSchemaForNobody01_NegativeFixture_StringLiteralOnly(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	fixturePath := filepath.Join(archDir, "testdata", "handler_no_schema_for_nobody_fixtures", "get_with_validator", "handler_gen.go")
	body, err := os.ReadFile(fixturePath) //nolint:gosec // archtest fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	text := string(body)
	if handlerEmbedsSchemaLiteral(text) {
		t.Errorf("HANDLER-NO-SCHEMA-FOR-NOBODY-01 negative fixture get_with_validator: " +
			"legacy strings.Contains FALSE-POSITIVES on comment/literal occurrences of " +
			"\"requestSchemaJSON\"; AST GREEN refactor required (scan *ast.GenDecl(VAR))")
	}
	if handlerWiresSchemaValidator(text) {
		t.Errorf("HANDLER-NO-SCHEMA-FOR-NOBODY-01 negative fixture get_with_validator: " +
			"legacy strings.Contains FALSE-POSITIVES on comment occurrences of " +
			"\"schemavalidate.NewValidator\"; AST GREEN refactor required (scan *ast.CallExpr)")
	}
}

// INVARIANT: HANDLER-PATH-QUERY-LENGTH-VALIDATION-01
//
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

// INVARIANT: HANDLER-POLICY-REQUIRED-01
//
// # HANDLER-POLICY-REQUIRED-01
//
// Invariant: every *contract.NewHandler(svc, nil) call in a cell.go that is
// NOT backed by a Public=true contract endpoint MUST supply a non-nil policy.
//
// Non-public routes with nil policy silently degrade to "no authorization
// guard" in production — any bearer token (valid JWT) can access the endpoint
// regardless of roles or ownership.
//
// This archtest scans cell.go files under:
//   - cells/**/cell.go
//   - examples/**/cells/**/cell.go
//
// and reports any NewHandler(_, nil) call where the corresponding generated
// handler package does NOT export a NewHandler that takes only one argument
// (the public-contract form, which takes no policy arg).
//
// Negative fixture: tools/archtest/testdata/handler_nil_policy/cell.go.
func TestHANDLER_POLICY_REQUIRED_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	cellFiles := collectCellGoFiles(t, root)

	var violations []string
	for _, f := range cellFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		hits := scanForNilPolicyNewHandler(f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: %s", handlerPolicyRequiredRule, v)
	}

	if len(violations) > 0 {
		t.Logf(`%s: %d violation(s) found. Fix: pass a non-nil auth.Policy to NewHandler.
For Public endpoints declare auth.public: true in contract.yaml and regenerate
— the generated NewHandler then accepts no policy argument, so the nil call
site disappears entirely.`, handlerPolicyRequiredRule, len(violations))
	}
}

// TestHANDLER_POLICY_REQUIRED_01_NegativeFixture verifies that the scanner
// detects the violation pattern in the negative fixture file.
func TestHANDLER_POLICY_REQUIRED_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixture := filepath.Join(root, "tools", "archtest", "testdata",
		"handler_nil_policy", "cell.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_nil_policy/cell.go"
	hits := scanForNilPolicyNewHandler(fixture, rel)
	if len(hits) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// collectCellGoFiles returns all cell.go files in cells/ and examples/ subtrees.
func collectCellGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string

	walk := func(dir string) {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			//nolint:nilerr // archtest walk skips unreadable nodes silently
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if info.Name() == "cell.go" {
				files = append(files, path)
			}
			return nil
		})
	}
	walk("cells")
	walk("examples")
	return files
}

// scanForNilPolicyNewHandler parses the Go file at path and returns a list of
// violation strings for any call of the form <pkg>.NewHandler(<expr>, nil)
// where <pkg> is not in handlerPolicyPublicExemptPkgs.
func scanForNilPolicyNewHandler(path, rel string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// Unparseable files are flagged as violations so the rule isn't silently skipped.
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "NewHandler" {
			return true
		}
		// Only inspect two-argument calls: NewHandler(svc, policy).
		if len(call.Args) != 2 {
			return true
		}
		// Check if the last argument is a nil identifier.
		ident, ok := call.Args[1].(*ast.Ident)
		if !ok || ident.Name != "nil" {
			return true
		}
		// Determine the package alias used for the call (e.g. "enqueuecontract").
		pkgAlias := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkgAlias = id.Name
		}
		// Skip known Public-contract packages.
		if isPublicExemptPkg(pkgAlias) {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: %s.NewHandler called with nil policy — non-public endpoint must supply a real auth.Policy",
			rel, pos.Line, pkgAlias,
		))
		return true
	})
	return violations
}

// isPublicExemptPkg returns true when pkgAlias belongs to a Public=true contract.
func isPublicExemptPkg(alias string) bool {
	for _, exempt := range handlerPolicyPublicExemptPkgs {
		if alias == exempt {
			return true
		}
	}
	return false
}

// INVARIANT: HANDLER-VALIDATOR-FAIL-FAST-01
//
// # HANDLER-VALIDATOR-FAIL-FAST-01
//
// Invariant: generated handler_gen.go files that embed requestSchemaJSON MUST
// use the fail-fast panic pattern in NewHandler, not the swallow pattern
// (if err == nil { h.requestValidator = v }).
//
// The swallow pattern is unsafe because a schema that fails to compile (e.g.
// invalid JSON or unsupported draft) causes requestValidator to remain nil.
// The nil-guard "if h.requestValidator != nil" then lets every request bypass
// schema validation — a silent security regression.
//
// Correct pattern (ref: k8s scheme.go init-panic; oapi-codegen strict):
//
//	v, err := schemavalidate.NewValidator(requestSchemaJSON)
//	if err != nil {
//	    panic(...)
//	}
//	h.requestValidator = v
//
// Banned pattern (fail-open):
//
//	if v, err := schemavalidate.NewValidator(requestSchemaJSON); err == nil {
//	    h.requestValidator = v
//	}
//
// Negative fixture: tools/archtest/testdata/handler_validator_fail_fast/violates/handler_gen.go.
func TestHANDLER_VALIDATOR_FAIL_FAST_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	genContractsDir := filepath.Join(root, "generated", "contracts")
	var handlerFiles []string
	_ = filepath.WalkDir(genContractsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // archtest skips unreadable nodes silently
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "handler_gen.go") {
			handlerFiles = append(handlerFiles, path)
		}
		return nil
	})

	if len(handlerFiles) == 0 {
		t.Fatalf("%s: no handler_gen.go files found under %s — scan broken",
			handlerValidatorFailFastRule, genContractsDir)
	}

	filesWithSchema := 0
	for _, f := range handlerFiles {
		rel, _ := filepath.Rel(root, f)
		violations := checkHandlerValidatorFailFast(f, rel)
		for _, v := range violations {
			t.Errorf("%s: %s", handlerValidatorFailFastRule, v)
		}
		// Count files that actually have requestSchemaJSON.
		if fileContainsRequestSchema(f) {
			filesWithSchema++
		}
	}

	if filesWithSchema == 0 {
		t.Fatalf("%s: no handler_gen.go with requestSchemaJSON found — scan logic broken",
			handlerValidatorFailFastRule)
	}
	t.Logf("%s: scanned %d handler_gen.go files, %d embed requestSchemaJSON",
		handlerValidatorFailFastRule, len(handlerFiles), filesWithSchema)
}

// TestHANDLER_VALIDATOR_FAIL_FAST_01_NegativeFixture verifies the scanner
// detects the swallow pattern in the negative fixture.
func TestHANDLER_VALIDATOR_FAIL_FAST_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixture := filepath.Join(root, "tools", "archtest", "testdata",
		"handler_validator_fail_fast", "violates", "handler_gen.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_validator_fail_fast/violates/handler_gen.go"
	violations := checkHandlerValidatorFailFast(fixture, rel)
	if len(violations) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// fileContainsRequestSchema reports whether a file references requestSchemaJSON.
// Uses a quick string search to avoid parsing files that have no schema.
func fileContainsRequestSchema(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered itself
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "requestSchemaJSON")
}

// checkHandlerValidatorFailFast parses the handler_gen.go file and checks the
// NewHandler function body for the fail-fast pattern. Returns violations found.
func checkHandlerValidatorFailFast(path, rel string) []string {
	// Quick pre-screen: skip files without requestSchemaJSON entirely.
	if !fileContainsRequestSchema(path) {
		return nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "NewHandler" || fn.Body == nil {
			continue
		}
		v := analyzeNewHandlerBody(fn.Body, rel, fset)
		violations = append(violations, v...)
	}
	return violations
}

// analyzeNewHandlerBody inspects a NewHandler function body AST for the
// banned swallow pattern and verifies the panic pattern is present.
func analyzeNewHandlerBody(body *ast.BlockStmt, rel string, fset *token.FileSet) []string {
	var violations []string
	hasPanic := false
	hasSwallow := false

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// Detect panic(...) call.
			if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				hasPanic = true
			}
		case *ast.IfStmt:
			// Detect the swallow pattern:
			// if v, err := schemavalidate.NewValidator(requestSchemaJSON); err == nil { ... }
			// Signature: IfStmt with Init (short var decl calling NewValidator) and
			// Cond that is a BinaryExpr "err == nil".
			if node.Init != nil && isNewValidatorInit(node.Init) && isErrEqNilCond(node.Cond) {
				pos := fset.Position(node.Pos())
				hasSwallow = true
				violations = append(violations, fmt.Sprintf(
					"%s:%d: NewHandler uses fail-open swallow pattern "+
						"(if v, err := schemavalidate.NewValidator(...); err == nil) — "+
						"schema compile failure silently skips validation; "+
						"use panic on err != nil (codegen invariant)",
					rel, pos.Line,
				))
			}
		}
		return true
	})

	// If the file has requestSchemaJSON but NewHandler doesn't panic, flag it
	// (unless we already flagged the swallow pattern above).
	if !hasPanic && !hasSwallow {
		violations = append(violations, fmt.Sprintf(
			"%s: NewHandler does not panic on schema compile failure — "+
				"use `if err != nil { panic(...) }` after schemavalidate.NewValidator",
			rel,
		))
	}
	return violations
}

// isNewValidatorInit returns true when stmt is a short variable declaration
// that calls schemavalidate.NewValidator or NewValidator.
func isNewValidatorInit(stmt ast.Stmt) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return false
	}
	if assign.Tok.String() != ":=" {
		return false
	}
	for _, rhs := range assign.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == "NewValidator" {
				return true
			}
		case *ast.Ident:
			if fn.Name == "NewValidator" {
				return true
			}
		}
	}
	return false
}

// isErrEqNilCond returns true when expr is a binary expression "err == nil".
func isErrEqNilCond(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	if bin.Op.String() != "==" {
		return false
	}
	ident, ok := bin.X.(*ast.Ident)
	if !ok {
		return false
	}
	nilIdent, ok := bin.Y.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "err" && nilIdent.Name == "nil"
}
