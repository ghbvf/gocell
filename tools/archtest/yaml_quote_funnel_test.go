// invariants asserted in this file:
//   - INVARIANT: YAML-QUOTE-FUNNEL-01
//
// YAML-QUOTE-FUNNEL-01: every type conversion `yamlsafe.Scalar(x)` outside
// the pkg/yamlsafe package itself must have x = `yamlsafe.Quote(...)` (or
// already typed as yamlsafe.Scalar) — raw string conversions bypass the
// single quoting funnel and reintroduce YAML injection via colons / braces /
// leading whitespace / metacharacters.
//
// AI-rebust: Hard (charter §1 string-typed concept funnel template). The
// conversion callee is resolved via *types.Info.Uses[ident] so a same-name
// local TypeName or alias cannot bypass the check; the argument expression
// is also resolved through *types.Info to its callee Func so only
// yamlsafe.Quote — not shadowed identifiers — satisfies the funnel.
//
// Blind spot inventory (covered by reverse self-test):
//   - bare ident form `Scalar(x)` inside pkg/yamlsafe itself (allowed,
//     skipped via Pkg.Path() == yamlsafePkgPath)
//   - selector form `yamlsafe.Scalar(x)` outside pkg/yamlsafe (common case)
//   - Arg shape: only direct `yamlsafe.Quote(x)` CallExpr is allowed (a value
//     of declared static type yamlsafe.Scalar is also allowed as a no-op
//     identity conversion, covering helpers that return a Scalar)
//   - reverse self-test fixture: scanner applied to pkg/yamlsafe production
//     AST (path filter bypassed) MUST report ≥3 bare Scalar(raw) sites
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
// ≥3 violations. The outer TestYAMLQuoteFunnel skips pkg/yamlsafe by path,
// so production stays clean; this test invokes the scanner with the path
// filter bypassed to exercise detection.
//
// If the scanner ever stops detecting these bare conversions — e.g. the
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
	require.NotEmpty(t, diags,
		"scanner must detect bare Scalar(raw) conversions inside yamlsafe.go "+
			"(Quote() body has three such sites); empty result means the "+
			"types.Info-based detection silently regressed")
	require.GreaterOrEqual(t, len(diags), 3,
		"expected ≥3 bare-conversion sites inside Quote(); scanner detected only %d: %v",
		len(diags), diags)
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
	if !ok || tn.Pkg() == nil {
		return false
	}
	return tn.Pkg().Path() == yamlsafePkgPath && tn.Name() == yamlsafeScalarType
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
func allowedScalarConversionArg(info *types.Info, arg ast.Expr) bool {
	if call, ok := arg.(*ast.CallExpr); ok {
		if isYAMLQuoteCall(info, call.Fun) {
			return true
		}
	}
	t := info.TypeOf(arg)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
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
