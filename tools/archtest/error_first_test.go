package archtest

// error_first_test.go enforces:
//   - ERROR-FIRST-API-01: in the explicitly enrolled files (PR-MODE-6 scope),
//     exported and unexported function declarations whose return signature
//     does NOT include an error MUST NOT contain a `panic(...)` call in the
//     function body.
//   - ERROR-FIRST-TYPED-NIL-01: error-returning New* constructors in the same
//     file scope must validate interface dependencies with
//     validation.IsNilInterface so typed-nil implementations fail at construction
//     time, or are normalized through the constructor's optional-default path.
//
// Auto-exemptions:
//   - Function name starts with "Must" (Go community convention for the
//     panic-on-misuse twin of an error-returning constructor)
//   - `func init()` (init cannot return error; package-level invariant violations
//     are by definition fatal)
//
// Function-level whitelist (architectural panic permitted):
//   - kernel/wrapper/lifecycle.go::recoverAndFinishWithRedactor — middle
//     of a `defer recover()` chain that re-panics so the outer Recovery
//     middleware can record + serialize the panic. Refactoring it to
//     error would dismantle the entire recover propagation idiom. Any
//     OTHER error-less function in lifecycle.go that contains panic() is
//     still reported as a violation.
//   - runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure —
//     middle of a `defer recover()` chain that records breaker failure before
//     re-panicking so the outer Recovery middleware remains the single
//     panic-to-HTTP and panic-to-tracing boundary.
//
// Enforced file scope (PR-MODE-6 + PR-MODE-6.1):
//   - kernel/wrapper/handler.go, consumer.go, spec.go, lifecycle.go (whitelisted)
//   - kernel/cell/auth_plan.go
//   - kernel/outbox/entry_id.go, envelope.go
//   - kernel/idempotency/inmem.go
//   - kernel/worker/worker.go
//   - runtime/eventrouter/router.go, contract_middleware.go
//   - runtime/auth/route.go
//   - runtime/worker/worker.go
//   - runtime/distlock/locker.go
//   - runtime/auth/refresh/memstore/store.go
//   - runtime/http/middleware/circuit_breaker.go
//   - runtime/http/health/health.go
//   - runtime/http/router/router.go
//   - kernel/persistence/tx.go
//   - cells/accesscore/slices/sessionlogin/service.go
//   - cells/accesscore/slices/sessionrefresh/service.go
//   - cells/accesscore/slices/sessionlogout/service.go
//   - adapters/postgres/refresh_store.go

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const ruleErrorFirstAPI01 = "ERROR-FIRST-API-01"
const ruleErrorFirstTypedNil01 = "ERROR-FIRST-TYPED-NIL-01"

// errorFirstEnforcedFiles are the relative paths (from module root) of files
// whose declarations must satisfy ERROR-FIRST-API-01. Slash-separated for
// portability; converted with filepath.FromSlash before stat.
var errorFirstEnforcedFiles = []string{
	"kernel/wrapper/handler.go",
	"kernel/wrapper/consumer.go",
	"kernel/wrapper/spec.go",
	"kernel/wrapper/lifecycle.go",
	"kernel/cell/auth_plan.go",
	"kernel/outbox/entry_id.go",
	"kernel/outbox/envelope.go",
	"kernel/idempotency/inmem.go",
	"kernel/worker/worker.go",
	"runtime/eventrouter/router.go",
	"runtime/eventrouter/contract_middleware.go",
	"runtime/auth/route.go",
	"runtime/worker/worker.go",
	"runtime/distlock/locker.go",
	"runtime/auth/refresh/memstore/store.go",
	"runtime/http/middleware/circuit_breaker.go",
	"runtime/http/health/health.go",
	"runtime/http/router/router.go",
	"kernel/persistence/tx.go",
	"cells/accesscore/slices/sessionlogin/service.go",
	"cells/accesscore/slices/sessionrefresh/service.go",
	"cells/accesscore/slices/sessionlogout/service.go",
	"adapters/postgres/refresh_store.go",
}

// errorFirstViolation describes a single ERROR-FIRST-API-01 violation.
type errorFirstViolation struct {
	File     string // relative slash path from module root
	Line     int
	FuncName string
	Reason   string
}

// TestErrorFirstAPI01 walks the enforced file list and reports panic() calls
// inside error-less function declarations.
func TestErrorFirstAPI01(t *testing.T) {
	root := findModuleRoot(t)

	var violations []errorFirstViolation
	for _, rel := range errorFirstEnforcedFiles {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		v := scanFileForErrorFirstViolations(t, abs, rel)
		violations = append(violations, v...)
	}

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleErrorFirstAPI01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s — %s", v.File, v.Line, v.FuncName, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: error-less functions must not contain panic(); use error-returning signature, "+
			"rename to Must*, or add an ADR-justified file-level whitelist entry "+
			"(see docs/architecture/202604270030-architectural-panic-whitelist.md)",
		ruleErrorFirstAPI01)
}

func TestErrorFirstTypedNil01(t *testing.T) {
	root := findModuleRoot(t)

	violations := scanErrorFirstConstructorsForTypedNilGuards(t, root)

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleErrorFirstTypedNil01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s — %s", v.File, v.Line, v.FuncName, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: error-first constructors must validate interface dependencies with "+
			"validation.IsNilInterface(param), so typed-nil implementations fail at construction time",
		ruleErrorFirstTypedNil01)
}

func TestErrorFirstTypedNilScannerFixtures(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantLines []int
	}{
		{
			name: "constructor interface param without IsNilInterface fails",
			src: `package p
type Dep interface{ Do() }
func New(dep Dep) (*Service, error) {
	if dep == nil {
		return nil, nil
	}
	return &Service{}, nil
}
type Service struct{}`,
			wantLines: []int{3},
		},
		{
			name: "constructor interface param with IsNilInterface passes",
			src: `package p
var validation = struct{ IsNilInterface func(any) bool }{}
type Dep interface{ Do() }
func New(dep Dep) (*Service, error) {
	if validation.IsNilInterface(dep) {
		return nil, nil
	}
	return &Service{}, nil
}
type Service struct{}`,
		},
		{
			name: "optional interface param default with IsNilInterface passes",
			src: `package p
var validation = struct{ IsNilInterface func(any) bool }{}
type Reader interface{ Read([]byte) (int, error) }
type defaultReader struct{}
func (defaultReader) Read([]byte) (int, error) { return 0, nil }
func New(reader Reader) (*Service, error) {
	if validation.IsNilInterface(reader) {
		reader = defaultReader{}
	}
	return &Service{}, nil
}
type Service struct{}`,
		},
		{
			name: "non error returning constructor is outside error-first typed-nil rule",
			src: `package p
type Dep interface{ Do() }
func New(dep Dep) *Service {
	return &Service{}
}
type Service struct{}`,
		},
		{
			name: "non constructor function is outside typed-nil rule",
			src: `package p
type Dep interface{ Do() }
func Build(dep Dep) (*Service, error) {
	return &Service{}, nil
}
type Service struct{}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "p.go", tc.src, parser.SkipObjectResolution|parser.ParseComments)
			require.NoError(t, err)
			info := types.Info{
				Types: map[ast.Expr]types.TypeAndValue{},
				Defs:  map[*ast.Ident]types.Object{},
				Uses:  map[*ast.Ident]types.Object{},
			}
			conf := types.Config{Importer: nil}
			_, err = conf.Check("p", fset, []*ast.File{file}, &info)
			require.NoError(t, err)

			violations := scanTypedNilGuardsInFile(fset, &info, file, "p.go")
			var gotLines []int
			for _, v := range violations {
				gotLines = append(gotLines, v.Line)
			}
			assert.Equal(t, tc.wantLines, gotLines)
		})
	}
}

// scanFileForErrorFirstViolations parses a single Go source file and returns
// any panic() call inside an error-less function (excluding Must*-prefixed
// functions and init).
func scanFileForErrorFirstViolations(t *testing.T, abs, rel string) []errorFirstViolation {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var violations []errorFirstViolation
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if isInitFunc(fd) {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Must") {
			continue
		}
		if signatureReturnsError(fd.Type.Results) {
			continue
		}
		whitelistKey := rel + "::" + fd.Name.Name
		if _, whitelisted := architecturalPanicWhitelist[whitelistKey]; whitelisted {
			continue
		}
		findPanicCalls(fd.Body, func(callPos token.Pos) {
			violations = append(violations, errorFirstViolation{
				File:     rel,
				Line:     fset.Position(callPos).Line,
				FuncName: fd.Name.Name,
				Reason:   "function does not return error but contains panic()",
			})
		})
	}
	return violations
}

func scanErrorFirstConstructorsForTypedNilGuards(t *testing.T, root string) []errorFirstViolation {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(root, errorFirstPackagePatterns()...)
	require.NoError(t, err, "packages.Load")
	require.Empty(t, errs, "packages.Load type errors")

	enforced := errorFirstEnforcedFileMap(root)
	var violations []errorFirstViolation
	for _, pkg := range pkgs {
		violations = append(violations, scanTypedNilGuardsInPackage(pkg, enforced)...)
	}
	return violations
}

func scanTypedNilGuardsInPackage(pkg *packages.Package, enforced map[string]string) []errorFirstViolation {
	var violations []errorFirstViolation
	for i, file := range pkg.Syntax {
		if i >= len(pkg.CompiledGoFiles) {
			continue
		}
		abs := filepath.Clean(pkg.CompiledGoFiles[i])
		rel, ok := enforced[abs]
		if !ok {
			continue
		}
		violations = append(violations, scanTypedNilGuardsInFile(pkg.Fset, pkg.TypesInfo, file, rel)...)
	}
	return violations
}

func scanTypedNilGuardsInFile(fset *token.FileSet, info *types.Info, file *ast.File, rel string) []errorFirstViolation {
	var violations []errorFirstViolation
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !isErrorFirstConstructor(fd) {
			continue
		}
		for _, paramName := range interfaceParamNames(info, fd) {
			if hasValidationIsNilInterfaceGuard(fd.Body, paramName) {
				continue
			}
			violations = append(violations, errorFirstViolation{
				File:     rel,
				Line:     fset.Position(fd.Pos()).Line,
				FuncName: fd.Name.Name,
				Reason:   "interface dependency " + paramName + " is not validated with validation.IsNilInterface",
			})
		}
	}
	return violations
}

func errorFirstPackagePatterns() []string {
	dirs := make(map[string]struct{})
	for _, rel := range errorFirstEnforcedFiles {
		dirs[filepath.Dir(filepath.FromSlash(rel))] = struct{}{}
	}
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		patterns = append(patterns, "./"+filepath.ToSlash(dir))
	}
	sort.Strings(patterns)
	return patterns
}

func errorFirstEnforcedFileMap(root string) map[string]string {
	out := make(map[string]string, len(errorFirstEnforcedFiles))
	for _, rel := range errorFirstEnforcedFiles {
		out[filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))] = rel
	}
	return out
}

func isErrorFirstConstructor(fd *ast.FuncDecl) bool {
	return fd.Recv == nil &&
		strings.HasPrefix(fd.Name.Name, "New") &&
		signatureReturnsError(fd.Type.Results)
}

func interfaceParamNames(info *types.Info, fd *ast.FuncDecl) []string {
	if info == nil || fd.Type.Params == nil {
		return nil
	}
	var names []string
	for _, field := range fd.Type.Params.List {
		if !isInterfaceType(info.TypeOf(field.Type)) {
			continue
		}
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			names = append(names, name.Name)
		}
	}
	return names
}

func isInterfaceType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	_, ok := typ.Underlying().(*types.Interface)
	return ok
}

func hasValidationIsNilInterfaceGuard(body *ast.BlockStmt, paramName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "IsNilInterface" {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "validation" {
			return true
		}
		argIdent, ok := call.Args[0].(*ast.Ident)
		if !ok || argIdent.Name != paramName {
			return true
		}
		found = true
		return false
	})
	return found
}

// isInitFunc returns true if fd is `func init()` (no receiver, no params, no
// return values, name "init").
func isInitFunc(fd *ast.FuncDecl) bool {
	if fd.Name.Name != "init" {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	return true
}

// signatureReturnsError returns true if the FieldList contains at least one
// field whose type is the identifier `error` (built-in) — handles single
// return, named returns, and tuple returns.
func signatureReturnsError(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, field := range results.List {
		if isErrorIdent(field.Type) {
			return true
		}
	}
	return false
}

// isErrorIdent returns true when expr is the unqualified identifier `error`.
// Qualified types (e.g., pkg.MyError) and pointer/slice/array wrappers are
// intentionally rejected — only the built-in `error` interface satisfies the
// rule.
func isErrorIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "error"
}

// findPanicCalls walks body and invokes onPanic for every call to the built-in
// `panic` function. Calls inside nested function literals are also reported —
// a closure that panics still violates the rule unless the enclosing function
// returns error (which would let the closure propagate the failure instead).
//
// Built-in panic detection: the rule matches `panic(...)` where the Fun is the
// unqualified identifier `panic`. Re-defined locals (e.g. `var panic = func()`)
// would shadow the built-in; we treat them the same as the built-in to keep
// the rule conservative — there is no production reason to shadow `panic`.
func findPanicCalls(body *ast.BlockStmt, onPanic func(token.Pos)) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "panic" {
			onPanic(call.Pos())
		}
		return true
	})
}
