// invariants asserted in this file:
//   - INVARIANT: CELLGEN-ERRCODE-FUNNEL-01
//
// Package archtest — cellgen errcode funnel invariant.
//
// CELLGEN-ERRCODE-FUNNEL-01: every error-construction callsite in
// non-test .go files under tools/codegen/cellgen/ must use pkg/errcode.
// Concretely, in each cellgen production *ast.CallExpr:
//
//   - STEP 1 — callee resolution via *types.Info covers SelectorExpr
//     (pkg.Func, x.Method), bare Ident (dot-import callees,
//     function-valued vars after re-export), and ParenExpr unwrap.
//   - STEP 2 — known-constructor blacklist: if callee resolves to a
//     *types.Func with (Pkg.Path, Name) ∈ {(fmt, Errorf), (errors, New),
//     (errors, Join)}, fail. This is the OSS-aligned industry ceiling
//     per Kubernetes / Kratos / go-zero practice (custom analyzer with
//     types.Info call-resolution).
//   - STEP 3 — function-valued *types.Var with a signature returning a
//     single error is rejected, closing the indirect-addressing escape
//     route (e.g., `var ErrNew = errors.New; ErrNew("x")`).
//
// Form uniqueness comes from (callee.Pkg.Path, callee.Name) pair
// resolution under *types.Info — alias import / dot import / Ident /
// SelectorExpr / var indirection all collapse to the same identity check.
// Strictly dual to PANIC-REGISTERED-01.
//
// AI-rebust: Hard (form uniqueness + archtest fail-on-deviation; charter
// §"typed function call as Hard funnel for unbounded operations"
// recognized upper bound for "error-construction-callsite funnel" rule
// shape in Go). Funnel double-lock: upstream Hard via this archtest +
// depguard import ban on third-party error libraries (.golangci.yml);
// downstream Hard via ERRCODE-KIND-LITERAL-01 + MESSAGE-CONST-LITERAL-01
// + DETAILS-SLOG-ATTR-01 locking the pkg/errcode funnel content. See ADR
// docs/architecture/202605171200-adr-cellgen-errcode-funnel-mechanism.md
// §D5 (Path A canonical) + §D7 (Path C-full backlog upgrade path).
//
// Documented blind spots (charter §"工具选定后强制盲区自检"):
//   - Composite literal construction (`&myErr{}` where myErr implements
//     error in a non-errcode package): not detected at CallExpr level.
//     Currently absent in cellgen production (grep verified); future
//     hardening via Path C-full (errcode.Error field privatization)
//     backlogged as CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL.
//   - Reflect dynamic construction (reflect.MakeFunc → returns error):
//     not detectable from static AST; cellgen has zero `reflect.MakeFunc`
//     / `reflect.Value.Call` calls (grep verified).
//   - Third-party error libraries (e.g., github.com/pkg/errors): blocked
//     at import boundary by .golangci.yml depguard `cellgen-error-libs`
//     rule, so the (Pkg.Path, Name) blacklist doesn't need to enumerate
//     them.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

const ruleCellgenErrcodeFunnel01 = "CELLGEN-ERRCODE-FUNNEL-01"

// cellgenErrConstructorBlacklist is the OSS-aligned curated set of
// stdlib error-construction functions cellgen production code must not
// call. Form uniqueness is via the (Pkg.Path, Name) pair resolved through
// *types.Info, so aliased / dot-imported / re-exported invocations are
// caught identically to direct invocations. Third-party error libraries
// (github.com/pkg/errors, golang.org/x/xerrors, go.uber.org/multierr) are
// blocked at the import boundary by the .golangci.yml depguard
// `cellgen-error-libs` rule — defense in depth.
//
// Extending the blacklist: append a new (Pkg.Path, Name) entry below AND
// add a RED fixture under testdata/cellgen_errcode_funnel_fixtures/.
var cellgenErrConstructorBlacklist = []struct {
	PkgPath string
	Name    string
}{
	{PkgPath: "fmt", Name: "Errorf"},
	{PkgPath: "errors", Name: "New"},
	{PkgPath: "errors", Name: "Join"},
}

// errorInterface is the built-in `error` interface, looked up once from
// universe scope. Used for STEP 3 var-signature filtering. errcodePkgPath
// is declared in panic_invariants_test.go in the same archtest package and
// is reused zero-cost.
var errorInterface = func() *types.Interface {
	obj := types.Universe.Lookup("error")
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		panic("archtest: universe scope 'error' is not an interface")
	}
	return iface
}()

type cellgenErrcodeViolation struct {
	File   string
	Line   int
	Reason string
}

// scanFileForCellgenErrcodeViolations walks one AST file and returns
// CELLGEN-ERRCODE-FUNNEL-01 violations. info MUST be non-nil — *types.Info
// is required to resolve callees uniformly across SelectorExpr (pkg.Func),
// bare Ident (dot-import callees), and *types.Var (function-valued vars).
func scanFileForCellgenErrcodeViolations(
	p *Pass,
	file *ast.File,
	rel string,
) []cellgenErrcodeViolation {
	info := p.TypesInfo
	var violations []cellgenErrcodeViolation

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		obj, ok := resolveCellgenCallee(info, call.Fun)
		if !ok {
			return
		}
		switch o := obj.(type) {
		case *types.Func:
			if pkgPath, name, hit := blacklistMatch(o); hit {
				violations = append(violations, cellgenErrcodeViolation{
					File: rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Reason: fmt.Sprintf(
						"cellgen: error constructor outside errcode funnel: callee=%s from %s",
						name, pkgPath),
				})
			}
		case *types.Var:
			// STEP 3: function-valued var with signature returning a single
			// error is rejected (re-export indirect-addressing escape).
			sig, isSig := o.Type().(*types.Signature)
			if !isSig {
				return
			}
			results := sig.Results()
			if results.Len() != 1 || !typesutil.ImplementsInterface(results.At(0).Type(), errorInterface) {
				return
			}
			violations = append(violations, cellgenErrcodeViolation{
				File: rel,
				Line: p.Fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf(
					"cellgen: error constructor via function-valued variable forbidden: var=%s",
					o.Name()),
			})
		}
	})

	return violations
}

// blacklistMatch reports whether fn is in cellgenErrConstructorBlacklist.
// Returns the matched pkg path and name on hit.
func blacklistMatch(fn *types.Func) (pkgPath, name string, hit bool) {
	if fn.Pkg() == nil {
		return "", "", false
	}
	pkgPath = fn.Pkg().Path()
	name = fn.Name()
	for _, entry := range cellgenErrConstructorBlacklist {
		if entry.PkgPath == pkgPath && entry.Name == name {
			return pkgPath, name, true
		}
	}
	return "", "", false
}

// resolveCellgenCallee maps a CallExpr's Fun to its types.Object via
// types.Info. Covers SelectorExpr (pkg.Func, x.Method), bare Ident
// (dot-import callees, function-valued vars), and ParenExpr unwrap.
// Returns (nil, false) for FuncLit, type conversions, builtins
// (make/new/panic), and other non-Object forms.
func resolveCellgenCallee(info *types.Info, fun ast.Expr) (types.Object, bool) {
	switch e := fun.(type) {
	case *ast.SelectorExpr:
		if obj := info.Uses[e.Sel]; obj != nil {
			return obj, true
		}
	case *ast.Ident:
		if obj := info.Uses[e]; obj != nil {
			return obj, true
		}
	case *ast.ParenExpr:
		return resolveCellgenCallee(info, e.X)
	}
	return nil, false
}

// TestCellgenErrcodeFunnel enforces CELLGEN-ERRCODE-FUNNEL-01 against the
// production cellgen package (tools/codegen/cellgen/*.go, excluding
// *_test.go). Expected to pass: cellgen currently has zero fmt.Errorf /
// errors.New / errors.Join calls and uses pkg/errcode constructors
// throughout (verified by ADR spike, 2026-05-17).
//
// Narrower pattern "./tools/codegen/cellgen/..." obviates the
// KnownNonDefaultTags iteration used by module-wide rules — cellgen has no
// build-tag-gated files (verified: grep `//go:build` returns nothing).
func TestCellgenErrcodeFunnel(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []cellgenErrcodeViolation

	_ = RunTyped(t, TypedOpts{},
		[]string{"./tools/codegen/cellgen/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, v := range scanFileForCellgenErrcodeViolations(p, file, rel) {
					key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
					if _, dup := seen[key]; dup {
						continue
					}
					seen[key] = struct{}{}
					violations = append(violations, v)
				}
			}
			return nil
		})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleCellgenErrcodeFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
		t.Fatalf("%s: every error constructor in tools/codegen/cellgen must use pkg/errcode "+
			"(errcode.New / errcode.Wrap / errcode.Assertion / errcode.WrapInfra). "+
			"See ADR docs/architecture/202605171200-adr-cellgen-errcode-funnel-mechanism.md §D5.",
			ruleCellgenErrcodeFunnel01)
	}
}

// TestCellgenErrcodeFunnelScannerFixtures verifies the
// CELLGEN-ERRCODE-FUNNEL-01 rule logic against static fixture packages
// under tools/archtest/testdata/cellgen_errcode_funnel_fixtures/. Each
// fixture dir owns a diag.golden capturing the rule's real output. GREEN
// fixtures have an empty golden. Mirrors panic_registered_fixtures
// convention.
//
// Fixtures cover the full 7-class escape list from ADR §D5:
//
//	F1 fmt.Errorf normal SelectorExpr
//	F2 fmtx "fmt" alias import + fmtx.Errorf
//	F3 import . "fmt" + Errorf (bare Ident)
//	F4 errors.New normal SelectorExpr
//	F5 import . "errors" + New (bare Ident)
//	F6 function-valued var re-export
//	F7 cellgen-package self-built wrapper (inner blacklist hit)
//
// Plus one GREEN fixture exercising errcode.{Assertion, New, Wrap}.
func TestCellgenErrcodeFunnelScannerFixtures(t *testing.T) {
	t.Parallel()

	dirs := []string{
		"fmt_errorf_red",
		"fmt_alias_errorf_red",
		"fmt_dot_import_errorf_red",
		"errors_new_red",
		"errors_dot_import_new_red",
		"local_wrapper_red",
		"function_valued_var_red",
		"errcode_assertion_green",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/cellgen_errcode_funnel_fixtures/" + dir

			var diags []Diagnostic
			_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				for _, file := range p.Files {
					rel := p.Rel(file)
					for _, v := range scanFileForCellgenErrcodeViolations(p, file, rel) {
						diags = append(diags, Diagnostic{
							Rel:     v.File,
							Line:    v.Line,
							Message: v.Reason,
						})
					}
				}
				return nil
			})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"cellgen_errcode_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}
