// Importable rule body for TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01. Migrated
// from the legacy _test.go form to a non-test .go (M3 #1639) so the rule is
// module-path-agnostic — platform symbol paths are derived from
// [PlatformModulePath] (external.go), NOT bare "github.com/ghbvf/gocell…"
// literals (ARCHTEST-MODULE-PATH-FUNNEL-01). The dogfood + RED-fixture
// precision gate live in eval_predicate_centralization_test.go.
//
// Not registered in StandardCellRules: this is a framework-self-referential
// meta-rule that scans GoCell's own tools/archtest/*_test.go for
// constraint.Expr.Eval callsites. An external Cell repo has its own archtest
// package — the scan target (./tools/archtest/...) would resolve to that
// repo's archtest (if it exists) or yield zero files, making the rule
// vacuous-green there. Kept importable and module-path-agnostic (all platform
// paths derived from [PlatformModulePath]) but OUT of StandardCellRules (same
// disposition as the #1632 auth funnels and CAPABILITY-PROVIDER-FUNNEL-01).
//
// # TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01
//
// Every constraint.Expr.Eval(arg) callsite in tools/archtest/*_test.go (top-
// level package, excluding internal/ subpackages) MUST pass either:
//
//	Form A — a *ast.CallExpr whose callee resolves to either
//	         tools/archtest/internal/typeseval.BuildContextPredicate
//	         (qualified cross-package form) OR
//	         tools/archtest.BuildContextPredicate (same-package bare form,
//	         which is a thin delegation to typeseval.BuildContextPredicate).
//	         Any extraTags arguments are fine; the funnel is the call form.
//	Form B — an inline *ast.FuncLit of shape  func(_ string) bool { return false }
//	         — the all-false sentinel — body must be exactly one ReturnStmt
//	         whose single result is the identifier `false`. Indirection via a
//	         var binding (`var fn = func(...) { return false }; expr.Eval(fn)`)
//	         is NOT accepted: the inline FuncLit shape is the Hard surface.
//
// Any other predicate shape — `func(tag string) bool { return tag == "X" }`,
// a named helper, a variable identifier, a method reference, a composite
// return expression — fails this rule.
//
// Why a static funnel (Hard) and not a doc convention (Soft):
//
//   - The toolchain-default tag set (GOOS/GOARCH/cgo/unix/gc/go1.X) drifts
//     every release. A hand-written predicate that hard-codes "linux ||
//     darwin || amd64 || ..." silently misses additions. BuildContextPredicate
//     sources implicitDefaults from build.Default.ReleaseTags + a single
//     mirror of internal/syslist, so go.mod floor bumps propagate
//     automatically. Forcing every consumer through the call form makes the
//     drift unreachable.
//
//   - The all-false sentinel is the canonical "evaluate under empty tag
//     set" form; allowing it explicitly avoids forcing a contrived
//     BuildContextPredicate() call where the intent is to deny every tag.
//
// AI-robust rating: Hard. Per .claude/rules/gocell/ai-robust.md "Hard 范本"
// — "typed function call as Hard funnel for unbounded operations": form
// uniqueness + archtest fail-on-deviation is the highest grade reachable in
// Go for this rule shape. The Go type system does not prevent passing
// arbitrary func(string)bool to constraint.Expr.Eval at compile time;
// static archtest at CI is the canonical Hard enforcement.
//
// Covered (NOT a blind spot — listed to preempt the question): indirection
// through a var binding (`var f = func(...) bool { return false }; expr.Eval(f)`)
// resolves the arg to an *ast.Ident — neither the canonical BuildContextPredicate(...)
// call nor an all-false FuncLit — so it is REJECTED, forcing the inline canonical
// form (intentional strictness, not a gap).
//
// # Blind spots (BS)
//
//   - BS-1 Reflection / code generation producing constraint.Expr.Eval calls:
//     out of scope per ai-robust.md §3.
//   - BS-2 Scope: only top-level tools/archtest/ _test.go files are checked
//     (internal/ sub-packages are excluded per the rule intent).
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

const (
	evalPredicateRuleID = "TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01"

	// buildContextPredicatePkg is the canonical typeseval package. Derived from
	// PlatformModulePath so a module rename / /v2 bump updates one place
	// (ARCHTEST-MODULE-PATH-FUNNEL-01). Imports from external packages (outside
	// tools/archtest) must use this qualified form.
	buildContextPredicatePkg = PlatformModulePath + "/tools/archtest/internal/typeseval"
	// buildContextPredicateFacadePkg is the archtest package itself — same-package
	// test files (package archtest) call the unqualified facade wrapper
	// BuildContextPredicate which resolves here, not to typeseval. Both are
	// accepted canonical forms because the facade is a thin delegation. Derived
	// from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01).
	buildContextPredicateFacadePkg = PlatformModulePath + "/tools/archtest"
	buildContextPredicateFunc      = "BuildContextPredicate"
	// constraintExprPkgPath is the stdlib go/build/constraint package path.
	// This is intentionally a plain string literal (NOT derived from
	// PlatformModulePath) — it references a standard library package, not a
	// GoCell platform package.
	constraintExprPkgPath    = "go/build/constraint"
	constraintExprEvalMethod = "Eval"
)

// CheckEvalPredicateCentralization01 enforces
// TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01: every constraint.Expr.Eval callsite
// in tools/archtest/*_test.go (top-level package, excluding internal/ sub-
// packages) must pass either typeseval.BuildContextPredicate(...) (Form A) or
// an inline `func(_ string) bool { return false }` sentinel (Form B). It
// returns the diagnostics it observes; GoCell's TestEvalPredicateCentralization01
// calls it directly — single source, no parallel rule body.
func CheckEvalPredicateCentralization01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(TypedOpts{Tests: true}, []string{"./tools/archtest/..."}),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
					continue
				}
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, v := range scanFileForEvalPredicateViolations(p.Fset, file, p.TypesInfo, rel) {
					out = append(out, Diagnostic{
						Rel:     v.Rel,
						Line:    v.Line,
						Message: evalPredicateViolationMessage(v),
					})
				}
			}
			return out
		})
}

// evalPredicateViolationMessage formats the human-readable diagnostic message
// for a TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 violation.
func evalPredicateViolationMessage(v evalPredicateViolation) string {
	return "constraint.Expr.Eval predicate must be typeseval.BuildContextPredicate(...) " +
		"or inline func(_ string) bool { return false }; got " + v.Form + ". " +
		"Reason: the toolchain-default tag set (GOOS/GOARCH/cgo/unix/gc/go1.X) " +
		"drifts every release; hand-written predicates miss additions. Use " +
		"typeseval.BuildContextPredicate(extraTags...) to inherit defaults."
}

// evalPredicateViolation records one TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01
// hit for batched reporting.
type evalPredicateViolation struct {
	Rel  string
	Line int
	Form string // human-readable description of the offending shape
}

// scanFileForEvalPredicateViolations walks file's AST for calls shaped
// `<expr>.Eval(arg)` whose receiver static type is go/build/constraint.Expr,
// and records a violation for each call whose first argument matches neither
// Form A (typeseval.BuildContextPredicate(...) CallExpr) nor Form B (inline
// all-false sentinel FuncLit).
func scanFileForEvalPredicateViolations(
	fset *token.FileSet,
	file *ast.File,
	info *types.Info,
	rel string,
) []evalPredicateViolation {
	var violations []evalPredicateViolation

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isConstraintExprEvalCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			violations = append(violations, evalPredicateViolation{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Form: "<no arguments>",
			})
			return
		}
		arg := call.Args[0]

		// Form A: arg is a CallExpr whose callee resolves to
		// typeseval.BuildContextPredicate.
		if argCall, ok := arg.(*ast.CallExpr); ok {
			if isBuildContextPredicateCallee(argCall.Fun, info) {
				return
			}
			violations = append(violations, evalPredicateViolation{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Form: "CallExpr with non-canonical callee " + formatEvalCallee(argCall.Fun),
			})
			return
		}

		// Form B: arg is an inline FuncLit shaped func(_ string) bool { return false }.
		if fl, ok := arg.(*ast.FuncLit); ok {
			if isAllFalseSentinelFuncLit(fl) {
				return
			}
			violations = append(violations, evalPredicateViolation{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Form: "FuncLit with non-canonical body (not `return false` single-stmt)",
			})
			return
		}

		// Any other shape (Ident, SelectorExpr, etc.) — including indirection
		// through a var-bound predicate — is a violation. The inline FuncLit
		// requirement is what makes Form B Hard.
		violations = append(violations, evalPredicateViolation{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Form: "non-CallExpr/non-FuncLit predicate argument (var binding / Ident / SelectorExpr not allowed)",
		})
	})

	return violations
}

// isConstraintExprEvalCall reports whether call is `<expr>.Eval(...)` where
// the resolved method is go/build/constraint.Expr.Eval. Uses types.Info
// Selections (via typeseval.ResolveMethodCall) to recover the method's owning
// package and name. When info is nil (fixture / partial type info) the
// detection falls through to false — this is fail-closed for the
// caller-visible direction (we never over-flag a non-constraint Eval; the
// archtest may under-detect on missing type info, which manifests as the
// load failing earlier via require.NoError).
func isConstraintExprEvalCall(call *ast.CallExpr, info *types.Info) bool {
	if info == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	if sel.Sel.Name != constraintExprEvalMethod {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok {
		return false
	}
	if fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == constraintExprPkgPath && fn.Name() == constraintExprEvalMethod
}

// isBuildContextPredicateCallee reports whether funExpr refers to either
// typeseval.BuildContextPredicate (qualified, from external packages) or the
// archtest facade BuildContextPredicate (bare Ident, from same-package test
// files in package archtest). Uses types.Info via typeseval.ResolvePackageRef
// (handles both qualified `typeseval.X` and dot-imported bare `X` / same-pkg
// bare `X` forms); falls back to AST-only `typeseval.X` selector matching when
// info is nil. Mirrors panic_invariants_test.go::isApprovedCallee structure.
//
// Both accepted packages are canonical: buildContextPredicateFacadePkg wraps
// buildContextPredicatePkg as a thin delegation (archtest.BuildContextPredicate
// → typeseval.BuildContextPredicate). Same-package test files call the facade
// form and both resolve to the same semantic guarantee.
func isBuildContextPredicateCallee(funExpr ast.Expr, info *types.Info) bool {
	isCanonicalPkg := func(pkgPath string) bool {
		return pkgPath == buildContextPredicatePkg || pkgPath == buildContextPredicateFacadePkg
	}

	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		// Bare Ident is also possible under dot-import or same-package call;
		// ResolvePackageRef handles both *ast.SelectorExpr and *ast.Ident.
		if info != nil {
			if pkgPath, name, ok := ResolvePackageRef(info, funExpr); ok {
				return isCanonicalPkg(pkgPath) && name == buildContextPredicateFunc
			}
		}
		return false
	}
	if sel.Sel.Name != buildContextPredicateFunc {
		return false
	}
	if info != nil {
		if pkgPath, name, ok := ResolvePackageRef(info, sel); ok {
			return isCanonicalPkg(pkgPath) && name == buildContextPredicateFunc
		}
		// info is non-nil but ResolvePackageRef declined: sel.X is not a
		// package qualifier (e.g. method-position selector on a value with
		// a `BuildContextPredicate` method). Decline rather than falling
		// through to the AST-only path — that path matches any Ident named
		// "typeseval" and would over-accept under method-position shadowing.
		return false
	}
	// AST-only fallback (no info): match `typeseval.BuildContextPredicate`.
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return xIdent.Name == "typeseval"
}

// isAllFalseSentinelFuncLit reports whether fl is exactly:
//
//	func(<any-ident>) bool { return false }
//
// — body has exactly 1 statement, that statement is a ReturnStmt with
// exactly 1 result, and the result is the *ast.Ident `false`. Parameter
// and result types are not re-checked: the constraint.Expr.Eval signature
// already constrains them to func(string)bool at the type system.
//
// Equivalent forms like `return !true`, `return 0 == 1`, or a var binding
// (`var f = func(...) bool { return false }; expr.Eval(f)`) are NOT
// accepted. The inline-literal-with-`false`-ident shape is the Hard
// surface — anything else fails archtest.
//
// Note on `false` shadowing: Go permits redefining the predeclared
// `false` identifier via assignment (e.g. `false := true`), but only
// across multiple statements. The single-statement body check
// (len(fl.Body.List) == 1) structurally rules out a shadowing pattern,
// so `id.Name == "false"` is a complete check — there is no
// `{ false := true; return false }` form that fits in 1 statement.
func isAllFalseSentinelFuncLit(fl *ast.FuncLit) bool {
	if fl == nil || fl.Body == nil {
		return false
	}
	if len(fl.Body.List) != 1 {
		return false
	}
	ret, ok := fl.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	if len(ret.Results) != 1 {
		return false
	}
	id, ok := ret.Results[0].(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "false"
}

// formatEvalCallee returns a short human-readable callee for violation
// messages. Mirrors panic_invariants_test.go::formatCallee.
func formatEvalCallee(funExpr ast.Expr) string {
	switch fun := funExpr.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return "<unknown>"
		}
		if xIdent, ok := fun.X.(*ast.Ident); ok {
			return xIdent.Name + "." + fun.Sel.Name
		}
		return "?." + fun.Sel.Name
	}
	return "<unknown>"
}
