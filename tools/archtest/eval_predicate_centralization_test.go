package archtest

// INVARIANT: TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01
//
// eval_predicate_centralization_test.go — every go/build/constraint.Expr.Eval
// callsite in tools/archtest/*_test.go (top-level package, excluding
// internal/ subpackages) must pass either typeseval.BuildContextPredicate(...)
// or an inline `func(_ string) bool { return false }` sentinel. Hand-written
// tag predicates drift past go-toolchain default tag additions; this funnel
// makes the wrong shape archtest-detectable.
//
// Single-rule file per ai-collab.md "archtest 文件命名" branch (single rule →
// {rule}_test.go). Promote to {theme}_invariants_test.go if related TYPESEVAL-*
// invariants accumulate to ≥ 3.

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

const (
	buildContextPredicatePkg  = "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	buildContextPredicateFunc = "BuildContextPredicate"
	constraintExprPkgPath     = "go/build/constraint"
	constraintExprEvalMethod  = "Eval"
)

// INVARIANT: TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01
//
// Every constraint.Expr.Eval(arg) callsite in tools/archtest/*_test.go (top-
// level package, excluding internal/ subpackages) MUST pass either:
//
//	Form A — a *ast.CallExpr whose callee resolves to
//	         tools/archtest/internal/typeseval.BuildContextPredicate
//	         (any extraTags arguments are fine; the funnel is the call form).
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
// Implementation parallels PRODUCTION-LOADER-FUNNEL-01 (ban one shape,
// allowlist by structural exclusion) and PANIC-REGISTERED-01 (form
// uniqueness at panic() callsite).
//
// AI-rebust rating: Hard. Per .claude/rules/gocell/ai-collab.md "Hard 范本"
// — "typed function call as Hard funnel for unbounded operations": form
// uniqueness + archtest fail-on-deviation is the highest grade reachable in
// Go for this rule shape. The Go type system does not prevent passing
// arbitrary func(string)bool to constraint.Expr.Eval at compile time;
// static archtest at CI is the canonical Hard enforcement.
//
// Internal scope note: tools/archtest/internal/typeseval/buildtags_test.go
// is the implementor's coverage self-test for BuildContextPredicate's flat
// load semantics; it uses derived per-slice predicates by design and is
// outside this rule's scope (mirroring panic_invariants excluding
// pkg/panicregister/*_test.go from PANIC-REGISTERED-01).
func TestEvalPredicateCentralization01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// SharedResolver with tests=true loads *_test.go files so the rule can
	// inspect archtest tests themselves. Pattern is scoped to ./tools/archtest/...
	// — not the real-repo "./..." that PRODUCTION-LOADER-FUNNEL-01 polices —
	// so this is the canonical narrow-scope archtest-self load shape, same
	// as scanner_framework_usage_test.go.
	resolver, err := typeseval.SharedResolver(root, true, nil, "./tools/archtest/...")
	require.NoError(t, err, "typeseval.SharedResolver")

	var violations []evalPredicateViolation
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			// Scope: tools/archtest/*_test.go only (subpackages under
			// tools/archtest/internal/ are out of scope — see header doc).
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations,
				scanFileForEvalPredicateViolations(pkg.Fset, file, pkg.TypesInfo, rel)...)
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})
	for _, v := range violations {
		t.Errorf("TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01: %s:%d: "+
			"constraint.Expr.Eval predicate must be typeseval.BuildContextPredicate(...) "+
			"or inline func(_ string) bool { return false }; got %s. "+
			"Reason: the toolchain-default tag set (GOOS/GOARCH/cgo/unix/gc/go1.X) "+
			"drifts every release; hand-written predicates miss additions. Use "+
			"typeseval.BuildContextPredicate(extraTags...) to inherit defaults.",
			v.Rel, v.Line, v.Form)
	}
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

	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
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
	fn, ok := typeseval.ResolveMethodCall(info, sel)
	if !ok {
		return false
	}
	if fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == constraintExprPkgPath && fn.Name() == constraintExprEvalMethod
}

// isBuildContextPredicateCallee reports whether funExpr refers to
// typeseval.BuildContextPredicate. Uses types.Info via
// typeseval.ResolvePackageRef (handles both qualified `typeseval.X` and
// dot-imported bare `X` forms); falls back to AST-only `typeseval.X`
// selector matching when info is nil. Mirrors panic_invariants_test.go::
// isApprovedCallee structure.
func isBuildContextPredicateCallee(funExpr ast.Expr, info *types.Info) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		// Bare Ident is also possible under dot-import; ResolvePackageRef
		// handles both *ast.SelectorExpr and *ast.Ident.
		if info != nil {
			if pkgPath, name, ok := typeseval.ResolvePackageRef(info, funExpr); ok {
				return pkgPath == buildContextPredicatePkg && name == buildContextPredicateFunc
			}
		}
		return false
	}
	if sel.Sel.Name != buildContextPredicateFunc {
		return false
	}
	if info != nil {
		if pkgPath, name, ok := typeseval.ResolvePackageRef(info, sel); ok {
			return pkgPath == buildContextPredicatePkg && name == buildContextPredicateFunc
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

// TestEvalPredicateCentralizationFixtures is the "test the test" meta-test
// for TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 detection helpers. Each fixture
// package under
//
//	tools/archtest/testdata/eval_predicate_centralization_fixtures/<case>/
//
// is a synthetic single-file Go package that demonstrates one canonical
// shape (GREEN, 0 violations expected) or one violation shape (RED, the
// expected violation line(s) listed in the table below). Loading goes
// through typeseval.LoadPackages with full types.Info so the type-aware
// detection paths (isConstraintExprEvalCall via ResolveMethodCall;
// isBuildContextPredicateCallee via ResolvePackageRef) are exercised the
// same way as the main test.
//
// Adding a new fixture: drop a `<case>/usage.go` file with the demonstration
// shape and add a row to `cases` below. testdata/ is skipped by the Go
// toolchain so fixtures never run in default builds.
func TestEvalPredicateCentralizationFixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	fixtureBase := filepath.Join(root, "tools", "archtest", "testdata",
		"eval_predicate_centralization_fixtures")

	cases := []struct {
		dir       string
		wantLines []int // empty / nil = GREEN; non-empty = RED with these line numbers
	}{
		// GREEN — Form A and Form B canonical shapes accepted.
		{"form_a_good", nil},
		{"form_b_good", nil},
		// RED — hand-rolled predicate (most common drift form).
		{"inline_predicate_red", []int{10}},
		// RED — var binding indirection (arg is Ident, not CallExpr / FuncLit).
		{"var_binding_red", []int{10}},
		// RED — FuncLit body has multi-statement, fails sentinel single-stmt check.
		{"funclit_multi_stmt_red", []int{9}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()

			fixtureDir := filepath.Join(fixtureBase, tc.dir)
			pkgs, errs, err := typeseval.LoadPackages(root, false, nil,
				"./tools/archtest/testdata/eval_predicate_centralization_fixtures/"+tc.dir)
			require.NoError(t, err, "LoadPackages failed for fixture %s", tc.dir)
			require.Empty(t, errs, "package load errors for fixture %s: %v", tc.dir, errs)
			require.NotEmpty(t, pkgs, "no packages loaded for fixture %s", tc.dir)

			var violations []evalPredicateViolation
			for _, p := range pkgs {
				for i, file := range p.Syntax {
					if i >= len(p.GoFiles) {
						continue
					}
					abs := p.GoFiles[i]
					rel, ok := fileroles.Rel(fixtureDir, abs)
					if !ok {
						continue
					}
					violations = append(violations,
						scanFileForEvalPredicateViolations(p.Fset, file, p.TypesInfo, rel)...)
				}
			}

			var gotLines []int
			for _, v := range violations {
				gotLines = append(gotLines, v.Line)
			}
			sort.Ints(gotLines)

			wantLines := append([]int(nil), tc.wantLines...)
			sort.Ints(wantLines)

			assert.Equal(t, wantLines, gotLines,
				"fixture %s: violation lines mismatch (got: %+v)", tc.dir, violations)
		})
	}
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
