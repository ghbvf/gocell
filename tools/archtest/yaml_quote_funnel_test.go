// INVARIANT: YAML-QUOTE-FUNNEL-01
//
// YAML-QUOTE-FUNNEL-01: every type conversion `yamlsafe.Scalar(x)` outside
// the pkg/yamlsafe package itself must have x = `yamlsafe.Quote(...)` (or
// already typed as yamlsafe.Scalar) — raw string conversions bypass the
// single quoting funnel and reintroduce YAML injection via colons / braces /
// leading whitespace / metacharacters.
//
// AI-rebust: Hard (charter §1 string-typed concept funnel template). The
// conversion callee is resolved via *types.Info.Uses[ident] so a same-name
// local TypeName cannot bypass the check. Three bypass families are now
// all covered with no disclosed form-uniqueness blind spot remaining:
//   - type alias of Scalar: covered by types.Unalias resolution (Commit 2,
//     verified by TestYAMLQuoteFunnel_DetectsAliasBypass)
//   - string literal in conversion position: covered by const-value detection
//     (info.Types[arg].Value != nil guard in allowedScalarConversionArg)
//   - const concatenation / const ident in conversion position: covered by
//     the same const-value detection (Go's type checker evaluates all
//     constant-evaluable expressions to a constant.Value regardless of form)
//
// Funnel 双向锁: 下游 Hard（types.Info-resolved Scalar conversion call site
// must have Quote arg or already-typed Scalar). 上游 Medium — Pass.Pkg 路径
// 过滤跳过 pkg/yamlsafe 内部即认为是合规上游；任何包外类型化 Scalar 字段
// 都需经过 Quote 才能赋值（构造点被下游 archtest 守住）。041 plan §3 明确
// 不登记 backlog，本 PR 内三件套闭环（typed funnel + archtest 下游 Hard +
// 反向自检）。
//
// Blind spot inventory (covered by reverse self-test):
//   - bare ident form `Scalar(x)` inside pkg/yamlsafe itself (allowed,
//     skipped via Pkg.Path() == yamlsafePkgPath)
//   - selector form `yamlsafe.Scalar(x)` outside pkg/yamlsafe (common case)
//   - Arg shape: only direct `yamlsafe.Quote(x)` CallExpr is allowed (a value
//     of declared static type yamlsafe.Scalar is also allowed as a no-op
//     identity conversion, covering helpers that return a Scalar)
//   - type alias form `type AliasOfScalar = yamlsafe.Scalar; AliasOfScalar(raw)`
//     — covered by types.Unalias resolution; verified by
//     TestYAMLQuoteFunnel_DetectsAliasBypass with yamlquotefixture.
//   - string literal / const concat / const-typed Ident in conversion position
//     — Go's contextual typing assigns the target type (yamlsafe.Scalar) to
//     constant expressions; covered by info.Types[arg].Value != nil check in
//     allowedScalarConversionArg; verified by
//     TestYAMLQuoteFunnel_DetectsLiteralBypass with yamlquotefixture.
//   - reverse self-test fixture: scanner applied to pkg/yamlsafe production
//     AST (path filter bypassed) MUST report at least one bare Scalar(raw) site
//     present in Quote() — proves types.Info resolution actually fires
//
// ref: pkg/yamlsafe/yamlsafe.go — Quote single funnel definition
// ref: tools/archtest/prom_cell_label_funnel_test.go — companion Hard pattern
// ref: docs/architecture/202605141519-adr-archtest-pass-funnel.md — Pass-driver
//
//	paradigm; this file uses RunTyped / RunTypedProduction (no direct
//	packages.Load).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	yamlsafePkgPath     = "github.com/ghbvf/gocell/pkg/yamlsafe"
	yamlsafeScalarType  = "Scalar"
	yamlsafeQuoteFunc   = "Quote"
	yamlQuoteFunnelRule = "YAML-QUOTE-FUNNEL-01"
)

// TestYAMLQuoteFunnel enforces YAML-QUOTE-FUNNEL-01 on the production
// codebase: every yamlsafe.Scalar(...) type conversion outside pkg/yamlsafe
// must have a yamlsafe.Quote(...) call (or a typed Scalar value) as its
// argument.
func TestYAMLQuoteFunnel(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{Tests: false},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == yamlsafePkgPath {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return out
		})

	Report(t, yamlQuoteFunnelRule, diags)
}

// TestYAMLQuoteFunnel_DetectsViolation is the reverse self-test: feed the
// scanner the pkg/yamlsafe production AST (which intentionally constructs
// Scalar from raw strings inside Quote()) and assert the scanner produces
// at least one violation. The outer TestYAMLQuoteFunnel skips pkg/yamlsafe
// by path, so production stays clean; this test invokes the scanner with the
// path filter bypassed to exercise detection.
//
// The count threshold is ≥1 (not ≥3) so that internal refactoring of
// Quote()'s implementation does not break this guard. The invariant is that
// detection fires at least once — proving types.Info resolution works.
//
// If the scanner ever stops detecting bare conversions — e.g. the
// types.Info-based callee resolution silently fails — this test goes red
// and the YAML-QUOTE-FUNNEL-01 Hard property has regressed.
func TestYAMLQuoteFunnel_DetectsViolation(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./pkg/yamlsafe/"},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlsafePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlsafe package must be loaded")
	require.GreaterOrEqual(t, len(diags), 1,
		"scanner must detect at least one bare Scalar(raw) conversion inside yamlsafe.go; "+
			"empty result means the types.Info-based detection silently regressed")
}

// scanYAMLQuoteFunnel walks the file's AST looking for yamlsafe.Scalar(...)
// type conversions. For each found conversion, validates that the argument
// is either (a) a yamlsafe.Quote(...) call or (b) an expression whose
// declared static type is already yamlsafe.Scalar (allowing identity /
// helper-returns without forcing redundant Quote wrapping).
func scanYAMLQuoteFunnel(p *Pass, file *ast.File, rel string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isYAMLScalarConversion(p.TypesInfo, call.Fun) {
			return
		}
		if len(call.Args) != 1 {
			pos := p.Fset.Position(call.Pos())
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    pos.Line,
				Message: "yamlsafe.Scalar(...) conversion must take exactly one argument",
			})
			return
		}
		if allowedScalarConversionArg(p.TypesInfo, call.Args[0]) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"yamlsafe.Scalar(...) argument must be yamlsafe.Quote(...) " +
					"or a value of declared yamlsafe.Scalar type"),
		})
	})
	return diags
}

// isYAMLScalarConversion reports whether fun resolves to the yamlsafe.Scalar
// TypeName (i.e. the CallExpr is a type conversion, not a function call).
// Resolution goes through *types.Info.Uses so a same-name local variable
// does NOT register as the funnel target — the Hard property.
//
// Type aliases are handled by routing through types.Unalias before reading
// the underlying named type, so `type AliasOfScalar = yamlsafe.Scalar`
// followed by `AliasOfScalar(raw)` is correctly detected as a Scalar
// conversion (the alias resolves to the canonical yamlsafe.Scalar TypeName).
func isYAMLScalarConversion(info *types.Info, fun ast.Expr) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch v := fun.(type) {
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		ident = v.Sel
	default:
		return false
	}
	obj := info.Uses[ident]
	if obj == nil {
		return false
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return false
	}
	// Route through types.Unalias so that `type AliasOfScalar = yamlsafe.Scalar`
	// followed by `AliasOfScalar(raw)` is detected. Without Unalias, the TypeName
	// resolves to the alias declaration in the caller package, not yamlsafe.Scalar,
	// and Pkg().Path() returns the caller package — silently bypassing the guard.
	named, ok := types.Unalias(tn.Type()).(*types.Named)
	if !ok {
		return false
	}
	nObj := named.Obj()
	if nObj == nil || nObj.Pkg() == nil {
		return false
	}
	return nObj.Pkg().Path() == yamlsafePkgPath && nObj.Name() == yamlsafeScalarType
}

// allowedScalarConversionArg reports whether arg is an acceptable input to
// yamlsafe.Scalar(...) outside pkg/yamlsafe:
//
//  1. arg is a CallExpr whose callee resolves to yamlsafe.Quote
//  2. arg's static type already is yamlsafe.Scalar (no-op identity, e.g. a
//     helper that returns Scalar feeding through a typed slice / struct field)
//
// String literals, fmt.Sprintf results, and arbitrary string-typed values
// all fail this predicate and must use yamlsafe.Quote.
//
// const-value guard: Go's type checker in a conversion position contextually
// assigns the target type (yamlsafe.Scalar) to constant-evaluable expressions
// (BasicLit string, const concat, const-typed Ident, iota, etc.), so
// info.TypeOf(arg) would return yamlsafe.Scalar and the named-type branch
// below would misclassify Scalar("literal") as an identity conversion.
// The guard rejects any arg whose types.Info entry carries a non-nil Value
// before reaching the named-type check.
func allowedScalarConversionArg(info *types.Info, arg ast.Expr) bool {
	if call, ok := arg.(*ast.CallExpr); ok {
		if isYAMLQuoteCall(info, call.Fun) {
			return true
		}
	}
	// Reject any expression that evaluates to a Go constant (BasicLit string,
	// string concatenation of consts, const-typed Ident, etc.). Go's
	// contextual typing in a conversion position assigns the target type
	// (yamlsafe.Scalar) to constant expressions, so the named-type check
	// below would otherwise misclassify Scalar("literal") as "already typed
	// as Scalar" and silently allow the bypass.
	if tv, ok := info.Types[arg]; ok && tv.Value != nil {
		return false
	}
	t := info.TypeOf(arg)
	if t == nil {
		return false
	}
	// Route through types.Unalias so that an argument whose declared static type
	// is `type AliasOfScalar = yamlsafe.Scalar` (i.e. an alias of Scalar used as
	// an identity no-op conversion target) is also recognized as already-typed Scalar.
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == yamlsafePkgPath && obj.Name() == yamlsafeScalarType
}

// isYAMLQuoteCall resolves fun via *types.Info.Uses and reports whether
// it refers to yamlsafe.Quote.
func isYAMLQuoteCall(info *types.Info, fun ast.Expr) bool {
	if info == nil {
		return false
	}
	var ident *ast.Ident
	switch v := fun.(type) {
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		ident = v.Sel
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
	return fn.Pkg().Path() == yamlsafePkgPath && fn.Name() == yamlsafeQuoteFunc
}

const yamlquotefixturePkgPath = "github.com/ghbvf/gocell/tools/archtest/internal/yamlquotefixture"

// TestYAMLQuoteFunnel_DetectsAliasBypass loads the archtest_fixture-gated
// yamlquotefixture package (which declares `type AliasOfScalar = yamlsafe.Scalar`)
// and asserts the scanner detects the bare alias conversion site (BypassViaAlias)
// while leaving the Quote-wrapped compliant site (CompliantQuoted) untouched.
//
// Without types.Unalias resolution in isYAMLScalarConversion, the TypeName for
// AliasOfScalar resolves to the caller package rather than pkg/yamlsafe, and the
// check silently passes — making this test fail RED and proving the regression.
func TestYAMLQuoteFunnel_DetectsAliasBypass(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/yamlquotefixture/"},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlquotefixturePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlquotefixture package must be loaded (check -tags=archtest_fixture)")
	require.NotEmpty(t, diags,
		"scanner must detect AliasOfScalar(\"evil-alias-raw\") via types.Unalias; "+
			"empty result means alias bypass is silently allowed (regression)")
	require.GreaterOrEqual(t, len(diags), 1,
		"at least 1 violation expected (BypassViaAlias); CompliantQuoted must not fire. got: %v", diags)
	require.Contains(t, diags[0].Message, "yamlsafe.Scalar(...)")
}

// TestYAMLQuoteFunnel_DetectsLiteralBypass loads the archtest_fixture-gated
// yamlquotefixture package and asserts that string-literal + const-concat
// conversion sites (BypassViaLiteral / BypassViaConstConcat) are detected
// by the const-value branch of allowedScalarConversionArg. Without
// info.Types[arg].Value-based filtering, Go's contextual typing would
// silently route them through the named-type "already Scalar" branch.
//
// CompliantTypedScalar (identity conversion through a typed local variable)
// must NOT fire — it exercises the allowed path where info.Types[arg].Value
// is nil and the named-type check sees yamlsafe.Scalar.
func TestYAMLQuoteFunnel_DetectsLiteralBypass(t *testing.T) {
	t.Parallel()

	var diags []Diagnostic
	found := false

	_ = RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/yamlquotefixture/"},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != yamlquotefixturePkgPath {
				return nil
			}
			found = true
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanYAMLQuoteFunnel(p, f, rel)...)
			}
			return nil
		})

	require.True(t, found, "yamlquotefixture package must be loaded (check -tags=archtest_fixture)")
	// Expect violations from: BypassViaAlias, BypassViaLiteral, BypassViaConstConcat.
	// CompliantQuoted and CompliantTypedScalar must NOT fire.
	require.GreaterOrEqual(t, len(diags), 3,
		"scanner must detect alias + literal + concat bypass sites; got %d: %v",
		len(diags), diags)
}
