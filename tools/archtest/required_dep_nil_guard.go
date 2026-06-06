package archtest

// required_dep_nil_guard.go — importable required-dep nil-guard rule logic
// (#1640 M3 PR-9).
//
// Non-test home for REQUIRED-DEP-NIL-GUARD-01 scanner logic (A2, A3, A4
// sub-rules), so it can be compiled and run by an external Cell repository
// (Go never compiles a dependency's _test.go, so rule logic external repos
// must run cannot live in a _test.go file). GoCell's own Test* functions in
// required_dep_nil_guard_test.go dogfood the same Check* — single source, no
// parallel rule body.
//
// Sub-rules implemented here:
//
//   - REQUIRED-DEP-NIL-GUARD-01/A2: every NewXxx(*Service, error) constructor
//     in a file whose Service struct has a gocell:"required" field must call
//     validateRequired() exactly once, after the options loop, consuming the
//     error in the canonical form.
//   - REQUIRED-DEP-NIL-GUARD-01/A3: hand-written service.go files must not
//     call validation.IsNilInterface on a gocell:"required" field.
//   - REQUIRED-DEP-NIL-GUARD-01/A4: gocell struct tag values must be in
//     {"", "required"}.
//
// A1 (generator golden-lock using requireddepsgen) and B1/B2/B3/B5
// (blind-spot reverse self-checks) remain in required_dep_nil_guard_test.go.
//
// Dogfood tests: TestRequiredDepNilGuard_A2_CallsiteUniqueness,
// TestRequiredDepNilGuard_A3_HandwrittenIsNilInterfaceBan,
// TestRequiredDepNilGuard_A4_TagValueWhitelist
// (required_dep_nil_guard_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: scan scope is gocell-hardcoded
// (cells/slices paths) with no ConfigForExternalCell consumer-extension →
// vacuous/false-red externally; kept importable + module-path-agnostic +
// fork-safe.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen/requireddepsgen"
)

// ---------------------------------------------------------------------------
// Package-path constants (module-path-agnostic)
// ---------------------------------------------------------------------------

// validationPkgPath is the import path of pkg/validation.
// Derived from PlatformModulePath so a module rename / /v2 bump updates exactly
// one place.
const validationPkgPath = PlatformModulePath + "/pkg/validation"

// isNilInterfaceFunc is the name of the banned helper function in pkg/validation.
const isNilInterfaceFunc = "IsNilInterface"

// requiredDepNilGuardRule is the rule ID for diagnostic messages.
const requiredDepNilGuardRule = "REQUIRED-DEP-NIL-GUARD-01"

// ---------------------------------------------------------------------------
// REQUIRED-DEP-NIL-GUARD-01 / A2
// ---------------------------------------------------------------------------

// CheckRequiredDepNilGuardA2 verifies that in every service.go (in slices/ or
// internal/ directories) whose Service struct has a gocell:"required" field,
// every NewXxx constructor calls validateRequired() exactly once, after the
// options loop, consuming the error canonically.
//
// Not registered in StandardCellRules: see file godoc.
func CheckRequiredDepNilGuardA2(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !isServiceGoForScan(rel) {
				continue
			}
			out = append(out, scanA2(p, file)...)
		}
		return out
	})
}

// scanA2 scans one file for A2 violations. When the file declares a Service
// struct carrying at least one gocell:"required" field, every New* constructor
// returning *Service MUST: (1) return error as its last result, and (2) call
// validateRequired() exactly once, after the options loop, consuming the error
// in the canonical form `if err := s.validateRequired(); err != nil { return
// ..., err }`. Counting the call alone is insufficient — a discarded result
// (`_ = s.validateRequired()`), a bare expression statement, or a bare-*Service
// signature that cannot propagate the error would each let a required dep escape
// the funnel. This mirrors fx/dig: a construction-time error must abort
// construction, not merely be observed.
//
// Files whose Service struct has no required field are skipped: validateRequired
// is then a no-op (A1 guarantees no gen file in that case) and there is nothing
// to enforce.
func scanA2(p *Pass, file *ast.File) []Diagnostic {
	if !serviceStructHasRequiredField(file) {
		return nil
	}
	var out []Diagnostic
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv != nil || fn.Body == nil {
			return
		}
		if !constructorReturnsStarService(fn) {
			return
		}
		out = append(out, checkA2Constructor(p, file, fn)...)
	})
	return out
}

// checkA2Constructor returns the A2 diagnostic(s) for a single New*-returning-
// *Service constructor in a required-bearing Service file. At most one is
// returned (the first failing condition).
func checkA2Constructor(p *Pass, file *ast.File, fn *ast.FuncDecl) []Diagnostic {
	mk := func(msg string) []Diagnostic {
		return []Diagnostic{{
			Rel:     p.Rel(file),
			Line:    p.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf("REQUIRED-DEP-NIL-GUARD-01-A2: %s (%s)", msg, requiredDepNilGuardRule),
		}}
	}

	if !lastResultIsError(fn.Type.Results) {
		return mk("NewService returns *Service but the Service struct has gocell:\"required\" " +
			"fields; it must return error to propagate validateRequired()")
	}

	count, preOpts := countValidateRequiredCalls(fn.Body, p.TypesInfo, optsLoopEnd(fn))
	switch {
	case count == 0:
		return mk("NewService does not call validateRequired()")
	case count > 1:
		return mk("validateRequired() must be called exactly once")
	case preOpts:
		return mk("validateRequired() must be called AFTER the options loop (currently before opts apply)")
	case !validateRequiredErrorConsumed(fn.Body):
		return mk("validateRequired() result must be checked and returned " +
			"(if err := s.validateRequired(); err != nil { return ..., err })")
	}
	return nil
}

// optsLoopEnd returns the token.Pos of the end of the options range loop in fn,
// or token.NoPos when no options loop is found. An options loop has the shape:
//
//	for _, o := range opts { o(s) }
//
// where "opts" matches the last variadic parameter of the function (if any).
func optsLoopEnd(fn *ast.FuncDecl) token.Pos {
	params := fn.Type.Params
	if params == nil || len(params.List) == 0 {
		return token.NoPos
	}
	last := params.List[len(params.List)-1]
	if _, ok := last.Type.(*ast.Ellipsis); !ok {
		return token.NoPos
	}
	if len(last.Names) == 0 {
		return token.NoPos
	}
	optsName := last.Names[0].Name

	rs, ok := FindFirstChild[ast.RangeStmt](fn.Body, func(rs *ast.RangeStmt) bool {
		x, ok := rs.X.(*ast.Ident)
		return ok && x.Name == optsName
	})
	if !ok {
		return token.NoPos
	}
	return rs.End()
}

// constructorReturnsStarService returns true when fn is a top-level New*
// function whose FIRST return type is *Service.
func constructorReturnsStarService(fn *ast.FuncDecl) bool {
	if !strings.HasPrefix(fn.Name.Name, "New") {
		return false
	}
	results := fn.Type.Results
	if results == nil || len(results.List) == 0 {
		return false
	}
	star, ok := results.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Service"
}

// lastResultIsError reports whether the function's last result type is error.
func lastResultIsError(results *ast.FieldList) bool {
	if results == nil || len(results.List) == 0 {
		return false
	}
	last := results.List[len(results.List)-1]
	ident, ok := last.Type.(*ast.Ident)
	return ok && ident.Name == "error"
}

// serviceStructHasRequiredField reports whether the file declares a
// `type Service struct` with at least one gocell:"required" field.
func serviceStructHasRequiredField(file *ast.File) bool {
	return len(requiredFieldNames(file)) > 0
}

// validateRequiredErrorConsumed reports whether body contains the canonical
// guard `if err := s.validateRequired(); err != nil { return ..., err }`.
func validateRequiredErrorConsumed(body *ast.BlockStmt) bool {
	found := false
	EachInSubtree[ast.IfStmt](body, func(ifs *ast.IfStmt) {
		if isCanonicalValidateRequiredGuard(ifs) {
			found = true
		}
	})
	return found
}

// isCanonicalValidateRequiredGuard returns true when ifs has the canonical form:
// `if err := s.validateRequired(); err != nil { ... err ... }`.
func isCanonicalValidateRequiredGuard(ifs *ast.IfStmt) bool {
	assign, ok := ifs.Init.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	errIdent, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || errIdent.Name == "_" {
		return false
	}
	if !isValidateRequiredCallExpr(assign.Rhs[0]) {
		return false
	}
	bin, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	if !identHasName(bin.X, errIdent.Name) || !identHasName(bin.Y, "nil") {
		return false
	}
	return ifs.Body != nil && blockReferencesIdent(ifs.Body, errIdent.Name)
}

// isValidateRequiredCallExpr reports whether expr is a call to a method named
// validateRequired (e.g. s.validateRequired()).
func isValidateRequiredCallExpr(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "validateRequired"
}

// identHasName reports whether expr is an *ast.Ident with the given name.
func identHasName(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

// blockReferencesIdent reports whether any *ast.Ident named name appears in block.
func blockReferencesIdent(block *ast.BlockStmt, name string) bool {
	found := false
	EachInSubtree[ast.Ident](block, func(id *ast.Ident) {
		if id.Name == name {
			found = true
		}
	})
	return found
}

// countValidateRequiredCalls counts how many times validateRequired() is called
// as a method on a receiver within body, and reports whether any such call
// occurs before loopEnd.
func countValidateRequiredCalls(body *ast.BlockStmt, info *types.Info, loopEnd token.Pos) (count int, preOpts bool) {
	firstCallPos := token.NoPos
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if !isValidateRequiredCall(call, info) {
			return
		}
		count++
		if !firstCallPos.IsValid() {
			firstCallPos = call.Pos()
		}
	})
	if loopEnd.IsValid() && firstCallPos.IsValid() && firstCallPos < loopEnd {
		preOpts = true
	}
	return count, preOpts
}

// isValidateRequiredCall reports whether call is a method call to validateRequired.
func isValidateRequiredCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "validateRequired" {
		return false
	}
	if info == nil {
		return true
	}
	if fn, ok := info.Selections[sel]; ok {
		return fn.Obj().Name() == "validateRequired"
	}
	return true // AST-only fallback
}

// isServiceGoForScan returns true for service.go files in slice directories
// that should be scanned by the production A2 rule.
func isServiceGoForScan(rel string) bool {
	if strings.HasSuffix(rel, "_gen.go") {
		return false
	}
	if !strings.HasSuffix(rel, "/service.go") {
		return false
	}
	return strings.Contains(rel, "/slices/") || strings.Contains(rel, "/internal/")
}

// ---------------------------------------------------------------------------
// REQUIRED-DEP-NIL-GUARD-01 / A3
// ---------------------------------------------------------------------------

// CheckRequiredDepNilGuardA3 verifies that no hand-written service.go calls
// validation.IsNilInterface to guard a gocell:"required" field.
//
// Not registered in StandardCellRules: see file godoc.
func CheckRequiredDepNilGuardA3(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !isHandWrittenServiceFile(rel) {
				continue
			}
			out = append(out, scanA3(p, file)...)
		}
		return out
	})
}

// scanA3 scans one file for A3 violations: hand-written validation.IsNilInterface
// calls that guard a REQUIRED field.
func scanA3(p *Pass, file *ast.File) []Diagnostic {
	requiredFields := requiredFieldNames(file)
	if len(requiredFields) == 0 {
		return nil
	}
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isIsNilInterfaceCallee(call.Fun, p.TypesInfo) {
			return
		}
		if !callArgIsRequiredField(call, requiredFields) {
			return
		}
		out = append(out, Diagnostic{
			Rel:  p.Rel(file),
			Line: p.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"REQUIRED-DEP-NIL-GUARD-01-A3: hand-written validation.IsNilInterface call in service.go "+
					"bypasses generated funnel; remove the call — required-dep nil checks are generated "+
					"in service_required_gen.go via gocell:\"required\" tag (%s)", requiredDepNilGuardRule,
			),
		})
	})
	return out
}

// callArgIsRequiredField reports whether the call's first argument is a selector
// `<expr>.<field>` whose field is a gocell:"required" field of the Service struct.
func callArgIsRequiredField(call *ast.CallExpr, requiredFields map[string]bool) bool {
	if len(call.Args) == 0 {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return requiredFields[sel.Sel.Name]
}

// isIsNilInterfaceCallee reports whether funExpr refers to validation.IsNilInterface.
func isIsNilInterfaceCallee(funExpr ast.Expr, info *types.Info) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != isNilInterfaceFunc {
		return false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return false
		}
		return fn.Pkg().Path() == validationPkgPath
	}
	// AST-only fallback
	xIdent, ok := sel.X.(*ast.Ident)
	return ok && xIdent.Name == "validation"
}

// isHandWrittenServiceFile returns true for service.go files that are NOT generated.
func isHandWrittenServiceFile(rel string) bool {
	if strings.HasSuffix(rel, "_gen.go") {
		return false
	}
	return strings.HasSuffix(rel, "/service.go") &&
		(strings.Contains(rel, "/slices/") || strings.Contains(rel, "/internal/"))
}

// ---------------------------------------------------------------------------
// REQUIRED-DEP-NIL-GUARD-01 / A4
// ---------------------------------------------------------------------------

// CheckRequiredDepNilGuardA4 verifies that all gocell struct tag values in
// production source are in {"", "required"}.
//
// Not registered in StandardCellRules: see file godoc.
func CheckRequiredDepNilGuardA4(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	scope := ModuleScope(root)
	return Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasPrefix(rel, "tools/") ||
				strings.HasPrefix(rel, "generated/") ||
				strings.HasPrefix(rel, "vendor/") {
				continue
			}
			out = append(out, scanA4(p, file)...)
		}
		return out
	})
}

// scanA4 scans one file for A4 violations: unknown gocell tag values.
func scanA4(p *Pass, file *ast.File) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.StructType](file, func(st *ast.StructType) {
		for _, field := range st.Fields.List {
			if field.Tag == nil {
				continue
			}
			raw := strings.Trim(field.Tag.Value, "`")
			if !requireddepsgen.TagSyntaxValid(raw) {
				out = append(out, Diagnostic{
					Rel:  p.Rel(file),
					Line: p.Fset.Position(field.Pos()).Line,
					Message: fmt.Sprintf(
						"REQUIRED-DEP-NIL-GUARD-01-A4: malformed struct tag %q (reflect.StructTag.Get would silently drop it) (%s)",
						raw, requiredDepNilGuardRule,
					),
				})
				continue
			}
			val := reflect.StructTag(raw).Get("gocell")
			if val == "" || val == "required" {
				continue
			}
			out = append(out, Diagnostic{
				Rel:  p.Rel(file),
				Line: p.Fset.Position(field.Pos()).Line,
				Message: fmt.Sprintf(
					"REQUIRED-DEP-NIL-GUARD-01-A4: unknown gocell tag value %q (allowed: \"\", \"required\") (%s)",
					val, requiredDepNilGuardRule,
				),
			})
		}
	})
	return out
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// requiredFieldNames extracts the set of field names tagged gocell:"required"
// from the file's Service struct (returns empty when no Service struct exists).
func requiredFieldNames(file *ast.File) map[string]bool {
	out := make(map[string]bool)
	EachInChildren[ast.GenDecl](file, func(genDecl *ast.GenDecl) {
		if genDecl.Tok != token.TYPE {
			return
		}
		collectRequiredFromTypeDecl(genDecl, out)
	})
	return out
}

// collectRequiredFromTypeDecl scans TypeSpec entries in a TYPE GenDecl
// and adds gocell:"required" field names from the Service struct into out.
func collectRequiredFromTypeDecl(genDecl *ast.GenDecl, out map[string]bool) {
	EachInChildren[ast.TypeSpec](genDecl, func(ts *ast.TypeSpec) {
		if ts.Name.Name != "Service" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return
		}
		collectRequiredFields(st, out)
	})
}

// collectRequiredFields adds field names tagged gocell:"required" in st to out.
func collectRequiredFields(st *ast.StructType, out map[string]bool) {
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		raw := strings.Trim(f.Tag.Value, "`")
		if reflect.StructTag(raw).Get("gocell") != "required" {
			continue
		}
		for _, name := range f.Names {
			out[name.Name] = true
		}
	}
}

// selectorFieldComparedToNil returns the selector's field name when be has the
// shape `<expr>.<field> == nil` or `nil == <expr>.<field>` (likewise for !=);
// otherwise returns "".
func selectorFieldComparedToNil(be *ast.BinaryExpr) string {
	isNilIdent := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "nil"
	}
	selField := func(e ast.Expr) string {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return ""
		}
		return sel.Sel.Name
	}
	switch {
	case isNilIdent(be.Y):
		return selField(be.X)
	case isNilIdent(be.X):
		return selField(be.Y)
	default:
		return ""
	}
}

// formatNilCompare returns a short readable rendering of `s.X == nil`.
func formatNilCompare(be *ast.BinaryExpr) string {
	field := selectorFieldComparedToNil(be)
	if field == "" {
		return "<unknown>"
	}
	return "<recv>." + field + " " + be.Op.String() + " nil"
}

// isIsNilInterfaceIdentObj reports whether obj is validation.IsNilInterface.
func isIsNilInterfaceIdentObj(obj types.Object) bool {
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == validationPkgPath && fn.Name() == isNilInterfaceFunc
}
