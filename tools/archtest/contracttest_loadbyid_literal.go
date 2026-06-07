package archtest

// contracttest_loadbyid_literal.go — importable CONTRACTTEST-LOADBYID-LITERAL-01
// rule logic (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run it; Go never compiles
// a dependency's _test.go. GoCell's own Test* in
// contracttest_loadbyid_literal_test.go dogfood the same Check* (single source,
// no parallel rule body).
//
// # CONTRACTTEST-LOADBYID-LITERAL-01
//
// Every call to contracttest.LoadByID must supply a compile-time constant string
// as the third argument (the contract ID). Runtime-computed contract IDs are
// forbidden because they prevent static analysis tools (including
// CONTRACT-PATH-QUERY-COVERAGE-01) from associating a LoadByID call site with a
// specific contract.
//
// Tool: Run(t, Production(TypedOpts{Tests: true})) — resolves the callee via
// *types.Info.Uses against tests/contracttest.LoadByID for both the
// cross-package selector form (contracttest.LoadByID) and the same-package
// bare-ident form (LoadByID, used inside tests/contracttest's own test files).
// The third argument is then checked via EvaluateConstString, which accepts
// BasicLit / const-bound Ident / SelectorExpr-to-const / BinaryExpr-of-consts
// via go/types constant folding. Runtime forms (struct field access, function
// call, plain variable assignment) are rejected.
//
// # AI-robust grade
//
// Medium (typed AST + EvaluateConstString; form mirrors MESSAGE-CONST-LITERAL-01).
//
// # Not registered (register=none)
//
// Not registered in StandardCellRules: gocell-internal-layout funnel — the rule
// locks the contracttest package path (under the GoCell module), which does not
// exist in an external module. Running against an external repo produces
// vacuous-green (no LoadByID calls to scan). Enforced in GoCell via
// TestContracttestLoadByIDLiteral01.

import (
	"go/ast"
	"go/types"
	"testing"
)

// contracttestLoadByIDPkg is the import path of the contracttest package
// whose LoadByID function is under scrutiny. Derived from PlatformModulePath
// so a module rename updates exactly one place (ARCHTEST-MODULE-PATH-FUNNEL-01).
const contracttestLoadByIDPkg = PlatformModulePath + "/tests/contracttest"

// contracttestLoadByIDFunc is the function name locked by this rule.
const contracttestLoadByIDFunc = "LoadByID"

// CheckContracttestLoadByIDLiteral01 runs CONTRACTTEST-LOADBYID-LITERAL-01 over
// the running module and returns diagnostics. It does NOT call t.Errorf; the
// caller should funnel results through Report(t, "CONTRACTTEST-LOADBYID-LITERAL-01", ...).
//
// Not registered in StandardCellRules: gocell-internal-layout funnel; vacuous-green
// in an external module; enforced in GoCell via TestContracttestLoadByIDLiteral01.
func CheckContracttestLoadByIDLiteral01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Production(TypedOpts{Tests: true}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		return scanLoadByIDLiteralViolations(p)
	})
}

// scanLoadByIDLiteralViolations scans p.Files for contracttest.LoadByID calls
// whose third argument does not resolve to a compile-time constant string,
// and returns diagnostics for each violation.
func scanLoadByIDLiteralViolations(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if len(call.Args) < 3 {
				return
			}
			if !isLoadByIDCall(call.Fun, p.TypesInfo) {
				return
			}
			// Third argument (index 2) must resolve to a compile-time const
			// string via go/types constant folding (covers BasicLit, const
			// Ident, SelectorExpr to const, BinaryExpr of consts). Runtime
			// forms (struct field access, function call, plain variable)
			// fail EvaluateConstString and produce a diagnostic.
			if _, ok := EvaluateConstString(p.TypesInfo, call.Args[2]); ok {
				return // compliant
			}
			pos := p.Fset.Position(call.Args[2].Pos())
			diags = append(diags, Diagnostic{
				Rel:  p.Rel(file),
				Line: pos.Line,
				Message: "CONTRACTTEST-LOADBYID-LITERAL-01: contracttest.LoadByID third argument " +
					"must be a compile-time constant string; got runtime expression",
			})
		})
	}
	return diags
}

// isLoadByIDCall reports whether funExpr resolves (via *types.Info) to
// contracttest.LoadByID. Accepts both forms:
//   - cross-package selector: contracttest.LoadByID(...)
//   - same-package bare identifier: LoadByID(...) inside the contracttest
//     package's own test files.
//
// Same-package detection (the *ast.Ident branch) is required because the
// contracttest package owns several internal table-driven tests that bare-call
// LoadByID; without this branch they escape the rule entirely.
func isLoadByIDCall(funExpr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch fn := funExpr.(type) {
	case *ast.SelectorExpr:
		if fn.Sel == nil || fn.Sel.Name != contracttestLoadByIDFunc {
			return false
		}
		ident = fn.Sel
	case *ast.Ident:
		if fn.Name != contracttestLoadByIDFunc {
			return false
		}
		ident = fn
	default:
		return false
	}
	obj := info.Uses[ident]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == contracttestLoadByIDPkg && fn.Name() == contracttestLoadByIDFunc
}
