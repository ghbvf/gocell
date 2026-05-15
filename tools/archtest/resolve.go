// Package archtest resolve.go — façade helper re-exports for business archtest rules.
//
// This file completes the façade contract: after Stage 1.5, all business
// archtest *_test.go files need only import "…/tools/archtest" — zero direct
// imports of internal/scanner, internal/typeseval, or x/tools/go/packages.
//
// # Deliberately excluded: loader symbols
//
// The six loader symbols (LoadPackages, SharedResolver, LoadProductionPackages,
// Resolver, ProductionResolver, EachFileInPackage) are intentionally NOT
// re-exported here. They are reachable only via archtest.RunTyped, which wraps
// the SharedResolver cache and constructs a *Pass with *types.Package (not
// *packages.Package). This is the Hard defensive layer described in
// docs/architecture/202605141519-adr-archtest-pass-funnel.md: a business
// *_test.go that receives a *Pass cannot reach .Syntax or reconstruct a fresh
// packages.Load, so the INV-1 bug class (pairing AST nodes from one load with
// a *types.Info from a different load) is inexpressible at the compiler level.
//
// # Why helper re-exports are necessary (D2 rationale)
//
// Business archtest rules frequently need to:
//   - resolve a SelectorExpr / Ident to its (pkgPath, symbolName) pair
//     across qualified / alias / dot-import forms — ResolvePackageRef;
//   - identify the *types.Func a method call resolves to — ResolveMethodCall;
//   - evaluate a cross-package constant string — EvaluateConstString;
//   - enumerate build-tag groups for multi-tag SharedResolver loops —
//     FlatNonDefaultTags / KnownNonDefaultTags.
//
// Hand-rolling these patterns via raw go/types in each rule is error-prone
// (missed dot-import bare-Ident path, missed alias form, missed untyped const
// folding). The façade re-exports ensure all rules use the same INV-1-safe
// implementations that have been tested against the three import shapes in
// typeseval's own test suite.
//
// # PASS-FUNNEL-RESOLVE-01 (enforcement)
//
// The meta-archtest PASS-FUNNEL-RESOLVE-01 in pass_funnel_test.go bans
// business *_test.go files from calling the eight typeseval helpers or
// scanner.ImportBan directly. Files in archtestmeta.LegacyAllowlist are
// temporarily exempt during the Stage 2/3 migration window.
package archtest

import (
	"go/ast"
	"go/types"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// ImportBan describes a rule that forbids importing specific packages. It is a
// type alias for [scanner.ImportBan]; callers may use it interchangeably with
// the internal type. Use [ImportBan.Run] to execute the check against a [Scope].
//
// Re-exported so business archtest files that use ImportBan need only import
// "…/tools/archtest" — no direct import of internal/scanner required.
type ImportBan = scanner.ImportBan

// ResolvePackageRef returns the (pkgPath, name) tuple for a reference to a
// package-level symbol, covering two AST shapes:
//   - Qualified selector `pkg.Name` (e.g., scanner.EachFile)
//   - Bare identifier after a dot-import (e.g., EachFile after import . "…/scanner")
//
// Thin delegation to [typeseval.ResolvePackageRef]. See that function's godoc
// for nil-guard and return-false cases (method receivers, builtins, local funcs).
//
// info must come from the same packages.Load result that produced the AST nodes
// you are inspecting — this is guaranteed when info is pass.TypesInfo.
func ResolvePackageRef(info *types.Info, expr ast.Expr) (pkgPath, name string, ok bool) {
	return typeseval.ResolvePackageRef(info, expr)
}

// ResolveMethodCall returns the *types.Func that a method-call SelectorExpr
// resolves to, using info.Selections. Handles direct/pointer/promoted/alias
// receivers and method expressions.
//
// Thin delegation to [typeseval.ResolveMethodCall]. Returns (nil, false) for
// non-method selectors, field selectors, or nil inputs.
//
// info must come from the same packages.Load result that produced sel.
func ResolveMethodCall(info *types.Info, sel *ast.SelectorExpr) (*types.Func, bool) {
	return typeseval.ResolveMethodCall(info, sel)
}

// EvaluateConstString returns the compile-time string constant value of expr,
// or ("", false) when expr is not a constant string.
//
// Thin delegation to [typeseval.EvaluateConstString]. Covers BasicLit / Ident
// / SelectorExpr / BinaryExpr via go/types constant folding.
//
// info must come from the same packages.Load result that produced expr.
func EvaluateConstString(info *types.Info, expr ast.Expr) (string, bool) {
	return typeseval.EvaluateConstString(info, expr)
}

// FlatNonDefaultTags returns the union of all distinct non-empty build tags
// appearing in [KnownNonDefaultTags], sorted. Suitable for a single
// SharedResolver call carrying every tag at once.
//
// Thin delegation to [typeseval.FlatNonDefaultTags].
func FlatNonDefaultTags() []string {
	return typeseval.FlatNonDefaultTags()
}

// KnownNonDefaultTags returns the build tag combinations that gate test or
// production files in this repo. Each entry is a []string as accepted by
// [TypedOpts.Tags]. The nil entry represents the default build context.
//
// Thin delegation to [typeseval.KnownNonDefaultTags].
func KnownNonDefaultTags() [][]string {
	return typeseval.KnownNonDefaultTags()
}
