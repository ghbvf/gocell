package archtest

// error_first_test.go enforces ERROR-FIRST-API-01: in the explicitly enrolled
// files (PR-MODE-6 scope), exported and unexported function declarations whose
// return signature does NOT include an error MUST NOT contain a `panic(...)`
// call in the function body.
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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type typedNilConstructorRule struct {
	File       string
	FuncName   string
	ParamNames []string
}

var typedNilConstructorRules = []typedNilConstructorRule{
	{
		File:     "cells/accesscore/slices/sessionlogin/service.go",
		FuncName: "NewService",
		ParamNames: []string{
			"userRepo",
			"sessionRepo",
			"roleRepo",
			"refreshStore",
		},
	},
	{
		File:     "cells/accesscore/slices/sessionrefresh/service.go",
		FuncName: "NewService",
		ParamNames: []string{
			"sessionRepo",
			"roleRepo",
			"userRepo",
			"refreshStore",
		},
	},
	{
		File:     "cells/accesscore/slices/sessionlogout/service.go",
		FuncName: "NewService",
		ParamNames: []string{
			"sessionRepo",
			"refreshStore",
		},
	},
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

	var violations []errorFirstViolation
	for _, rule := range typedNilConstructorRules {
		abs := filepath.Join(root, filepath.FromSlash(rule.File))
		violations = append(violations, scanConstructorForTypedNilGuards(t, abs, rule)...)
	}

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

func scanConstructorForTypedNilGuards(t *testing.T, abs string, rule typedNilConstructorRule) []errorFirstViolation {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoErrorf(t, err, "%s: parse failed", rule.File)

	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name.Name != rule.FuncName {
			continue
		}
		target = fd
		break
	}
	require.NotNilf(t, target, "%s: expected function %s", rule.File, rule.FuncName)

	var violations []errorFirstViolation
	for _, paramName := range rule.ParamNames {
		if hasValidationIsNilInterfaceGuard(target.Body, paramName) {
			continue
		}
		violations = append(violations, errorFirstViolation{
			File:     rule.File,
			Line:     fset.Position(target.Pos()).Line,
			FuncName: rule.FuncName,
			Reason:   "interface dependency " + paramName + " is not validated with validation.IsNilInterface",
		})
	}
	return violations
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
