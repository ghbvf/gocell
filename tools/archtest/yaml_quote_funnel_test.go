// invariants asserted in this file:
//   - INVARIANT: YAML-QUOTE-FUNNEL-01
//
// YAML-QUOTE-FUNNEL-01: every type conversion `yamlsafe.Scalar(x)` outside
// the pkg/yamlsafe package itself must have x = `yamlsafe.Quote(...)` —
// raw string conversions bypass the single quoting funnel and reintroduce
// YAML injection via colons / braces / leading whitespace / metacharacters.
//
// AI-rebust: Hard (typed-function-call funnel + types.Info form uniqueness,
// charter §1 string-typed concept funnel template). The conversion callee
// is resolved via *types.Info.Uses[ident] so a same-name local TypeName or
// alias cannot bypass the check; the Arg expression is also resolved
// through *types.Info to its callee Func so only yamlsafe.Quote — not
// shadowed identifiers — satisfies the funnel.
//
// Blind spot inventory (covered by reverse self-test):
//   - bare ident form `Scalar(x)` inside pkg/yamlsafe itself (allowed)
//   - selector form `yamlsafe.Scalar(x)` outside pkg/yamlsafe (the common case)
//   - Arg shape: only direct `yamlsafe.Quote(x)` CallExpr is allowed (a value
//     of declared static type yamlsafe.Scalar already in scope is also
//     allowed as a no-op identity conversion, covering helpers that return
//     a Scalar without going through Quote — see allowedScalarConversionArg)
//   - reverse self-test fixture: bare string literal `Scalar("evil")` must
//     fire the violation
//
// ref: pkg/yamlsafe/yamlsafe.go — Quote single funnel definition
// ref: tools/archtest/prom_cell_label_funnel_test.go — companion Hard pattern
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

const (
	yamlsafePkgPath     = "github.com/ghbvf/gocell/pkg/yamlsafe"
	yamlsafeScalarType  = "Scalar"
	yamlsafeQuoteFunc   = "Quote"
	yamlQuoteFunnelRule = "YAML-QUOTE-FUNNEL-01"
)

// TestYAMLQuoteFunnel enforces YAML-QUOTE-FUNNEL-01 on the production
// codebase: every yamlsafe.Scalar(...) type conversion outside pkg/yamlsafe
// must have a yamlsafe.Quote(...) call as its argument.
func TestYAMLQuoteFunnel(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, "./...")
	require.NoError(t, err, "LoadPackages failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		// Skip pkg/yamlsafe itself — its internal helpers (Quote, doubleQuote)
		// construct Scalar from raw string by design.
		if p.PkgPath == yamlsafePkgPath {
			return
		}
		// Skip synthetic test packages; their PkgPath suffix is ".test" or
		// "_test" and they are duplicates of the same Syntax.
		if strings.HasSuffix(p.PkgPath, ".test") || strings.HasSuffix(p.PkgPath, "_test") {
			return
		}
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations,
				scanYAMLQuoteFunnel(p.TypesInfo, p.Fset, file, rel)...)
		}
	})

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s: yamlsafe.Scalar(...) type conversions outside pkg/yamlsafe "+
			"must have yamlsafe.Quote(...) as the argument.\n"+
			"Violations (%d):\n  %s",
			yamlQuoteFunnelRule, len(violations), strings.Join(violations, "\n  "))
	}
}

// TestYAMLQuoteFunnel_DetectsViolation is the reverse self-test: feed the
// scanner a file known to contain bare `Scalar(rawString)` conversions
// (pkg/yamlsafe/yamlsafe.go itself — Quote internally constructs Scalar
// from raw strings) and assert the scanner produces violations. The outer
// TestYAMLQuoteFunnel skips the yamlsafe package by path, so production
// stays clean; this test invokes scanYAMLQuoteFunnel directly to exercise
// detection without the path filter.
//
// Blind-spot coverage:
//   - bare string conversion form `Scalar(x)` where x is not a Quote call
//   - bare ident form (inside yamlsafe package): scanner must still flag
//   - production-realistic AST (uses real packages.LoadPackages, not synth)
//
// If the scanner ever stops detecting these bare conversions — e.g. the
// types.Info-based callee resolution silently fails to bind — this test
// goes red and the YAML-QUOTE-FUNNEL-01 Hard property has regressed.
func TestYAMLQuoteFunnel_DetectsViolation(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, "./pkg/yamlsafe/")
	require.NoError(t, err, "LoadPackages failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var (
		found      bool
		violations []string
	)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != yamlsafePkgPath {
			return
		}
		found = true
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations,
				scanYAMLQuoteFunnel(p.TypesInfo, p.Fset, file, rel)...)
		}
	})

	require.True(t, found, "yamlsafe package must be loaded")
	require.NotEmpty(t, violations,
		"scanner must detect bare Scalar(raw) conversions inside yamlsafe.go "+
			"(Quote() body has three such sites); empty result means the "+
			"types.Info-based detection silently regressed")
	require.GreaterOrEqual(t, len(violations), 3,
		"expected ≥3 bare-conversion sites inside Quote(); scanner detected only %d: %v",
		len(violations), violations)
}

// scanYAMLQuoteFunnel walks file's AST looking for yamlsafe.Scalar(...) type
// conversions. For each found conversion, validates that the argument is
// either (a) a yamlsafe.Quote(...) call or (b) an expression whose declared
// static type is already yamlsafe.Scalar (allowing identity / helper-returns
// without forcing redundant Quote wrapping).
func scanYAMLQuoteFunnel(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []string {
	var violations []string
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isYAMLScalarConversion(info, call.Fun) {
			return
		}
		if len(call.Args) != 1 {
			pos := fset.Position(call.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: yamlsafe.Scalar(...) conversion must take exactly one argument",
				rel, pos.Line))
			return
		}
		if allowedScalarConversionArg(info, call.Args[0]) {
			return
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: yamlsafe.Scalar(...) argument must be yamlsafe.Quote(...) "+
				"or a value of declared yamlsafe.Scalar type",
			rel, pos.Line))
	})
	return violations
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
	// Identity conversion case: arg's static type already is yamlsafe.Scalar.
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

