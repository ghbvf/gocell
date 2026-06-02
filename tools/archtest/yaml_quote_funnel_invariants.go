package archtest

// yaml_quote_funnel_invariants.go — importable YAML-QUOTE-FUNNEL-01 rule logic.
//
// This is the non-test home of the YAML-QUOTE-FUNNEL-01 scanner so rule logic
// can be shared between the production dogfood test and the fixture-based
// reverse self-tests in yaml_quote_funnel_test.go (single source). GoCell's
// own TestYAMLQuoteFunnel (yaml_quote_funnel_test.go) calls CheckYAMLQuoteFunnel
// — no parallel rule body.
//
// Platform-symbol paths (yamlsafePkgPath) are anchored to [PlatformModulePath]
// so a module rename updates exactly one place and the ratchet meta-archtest
// ARCHTEST-MODULE-PATH-FUNNEL-01 can prove no rule reintroduces a bare literal.

// YAML-QUOTE-FUNNEL-01: every type conversion `yamlsafe.Scalar(x)` outside
// the pkg/yamlsafe package itself must have x = `yamlsafe.Quote(...)` (or
// already typed as yamlsafe.Scalar) — raw string conversions bypass the
// single quoting funnel and reintroduce YAML injection via colons / braces /
// leading whitespace / metacharacters.
//
// AI-robust: Hard (charter §1 string-typed concept funnel template). The
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
// 本 PR 内三件套闭环（typed funnel + archtest 下游 Hard +
// 反向自检）。上游 Medium → Hard 终态（seal Scalar 构造使包外不可表达
// 裸转换）的升级路径追踪于 gh issue #1304。
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
//   - known gap: if pkg/yamlsafe.Quote() is ever reimplemented WITHOUT a bare
//     Scalar(raw) conversion, TestYAMLQuoteFunnel_DetectsViolation flips RED→
//     GREEN (it no longer has a live conversion to detect). That is a property
//     of yamlsafe's own implementation, outside this archtest's control domain;
//     the fixture-based DetectsAliasBypass / DetectsLiteralBypass tests keep
//     proving types.Info resolution independently of Quote()'s body.
//
// ref: pkg/yamlsafe/yamlsafe.go — Quote single funnel definition
// ref: tools/archtest/prom_cell_label_funnel_test.go — companion Hard pattern
// ref: docs/architecture/202605141519-adr-archtest-pass-funnel.md — Pass-driver
//
//	paradigm; this file uses Run(t, Typed(...)) / Run(t, Production(...)) (no
//	direct packages.Load).

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	// yamlsafePkgPath is the canonical import path of the yamlsafe package —
	// a GoCell platform symbol path, anchored to PlatformModulePath so a module
	// rename updates exactly one place and no bare literal appears here.
	yamlsafePkgPath = PlatformModulePath + "/pkg/yamlsafe"

	yamlsafeScalarType  = "Scalar"
	yamlsafeQuoteFunc   = "Quote"
	yamlQuoteFunnelRule = "YAML-QUOTE-FUNNEL-01"

	// yamlquotefixturePkgPath is the canonical import path of the yamlquotefixture
	// test fixture package — anchored to PlatformModulePath.
	yamlquotefixturePkgPath = PlatformModulePath + "/tools/archtest/internal/yamlquotefixture"
)

// CheckYAMLQuoteFunnel runs YAML-QUOTE-FUNNEL-01 over the running module's
// production code and returns its diagnostics. GoCell's TestYAMLQuoteFunnel
// calls it directly (single source, no parallel rule body). cfg.BuildTags
// is wired into the TypedOpts.Tags for the production scan.
//
// Deliberately NOT registered in [StandardCellRules]: the rule only constrains
// conversions of the gocell-internal pkg/yamlsafe.Scalar type, so it is vacuous
// for an external Cell repo that never imports yamlsafe. It stays importable
// (non-test file, single source) for the dogfood Test, but external_repo wiring
// is out of scope — see external.go [StandardCellRules] godoc + M3 issue #1302.
func CheckYAMLQuoteFunnel(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	return Run(t, Production(TypedOpts{Tests: false, Tags: cfg.BuildTags}),
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
}

// scanYAMLQuoteFunnel walks the file's AST looking for yamlsafe.Scalar(...)
// type conversions. For each found conversion, validates that the argument
// is either (a) a yamlsafe.Quote(...) call or (b) an expression whose
// declared static type is already yamlsafe.Scalar (allowing identity /
// helper-returns without forcing redundant Quote wrapping).
func scanYAMLQuoteFunnel(p *Pass, file *ast.File, rel string) []Diagnostic {
	// p.TypesInfo is guaranteed non-nil: runRulePasses skips any Pass whose
	// buildTypedPass returned nil (pkg.TypesInfo == nil), so every Pass that
	// reaches a typed rule like this one carries a populated TypesInfo. The
	// callee-resolution helpers below still nil-guard defensively in isolation.
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
			Message: "yamlsafe.Scalar(...) argument must be yamlsafe.Quote(...) " +
				"or a value of declared yamlsafe.Scalar type",
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
