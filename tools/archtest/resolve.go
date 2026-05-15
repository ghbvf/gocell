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
//     FlatNonDefaultTags / KnownNonDefaultTags;
//   - extract a file's build constraint expression for 3-way evaluation under
//     custom tag sets — ParseBuildConstraint;
//   - test whether a module-relative path is under generated/ — IsGeneratedRelPath.
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
	"go/build/constraint"
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

// BuildContextPredicate returns a tag predicate suitable for
// constraint.Expr.Eval. It returns true for any tag the Go toolchain sets
// implicitly under a standard CI context, plus any extraTags supplied by
// the caller.
//
// Use this when [Pass.IsFileInScope] is insufficient — e.g. when you need to
// evaluate a build constraint under a custom tag set such as "integration".
// Pass.IsFileInScope uses the default (no extra tags) predicate; if you need
// custom extra tags, call BuildContextPredicate("integration") and evaluate
// the constraint expression directly via [archtest.ParseBuildConstraint]:
//
//	expr, err := archtest.ParseBuildConstraint(path)
//	if err != nil || expr == nil { ... }
//	withTag := expr.Eval(archtest.BuildContextPredicate("integration"))
//	withoutTag := expr.Eval(archtest.BuildContextPredicate())
//
// Thin delegation to [typeseval.BuildContextPredicate]. See that function's
// godoc for the full implicit-defaults catalog (GOOS/GOARCH/cgo/unix/gc/go1.X).
func BuildContextPredicate(extraTags ...string) func(string) bool {
	return typeseval.BuildContextPredicate(extraTags...)
}

// ParseBuildConstraint extracts the file's build constraint expression so it
// can be evaluated under a custom tag set. Returns (nil, nil) when the file
// has no //go:build or // +build directive. Returns (nil, err) on parse
// failure (fail-closed).
//
// Typical 3-way evaluation pattern (mirrors build_constraint_test.go and
// ci_integration_discovery_invariants_test.go):
//
//	expr, err := archtest.ParseBuildConstraint(path)
//	if err != nil || expr == nil { ... }
//	withTag    := expr.Eval(archtest.BuildContextPredicate("integration"))
//	withoutTag := expr.Eval(archtest.BuildContextPredicate())
//	withNone   := expr.Eval(func(_ string) bool { return false })
//
// Use [Pass.IsFileInScope] when you only need the default-context (no extra
// tags) boolean result — it is simpler and does not expose the raw
// constraint.Expr. Use this function when you need the raw Expr for multi-
// predicate evaluation.
//
// Thin delegation to [typeseval.ParseBuildConstraint]. filePath must be an
// absolute OS-native path (pass.Abs(f) is a suitable source).
func ParseBuildConstraint(filePath string) (constraint.Expr, error) {
	return typeseval.ParseBuildConstraint(filePath)
}

// IsGeneratedRelPath reports whether rel is a codegen output path under the
// repo's generated/ tree. rel must be a module-relative slash path (as
// returned by pass.Rel(f) or pkgFileRel).
//
// Returns true when rel begins with "generated/" (top-level only). The repo
// reserves exactly one generated/ directory at module root; a "generated/"
// prefix inside a hand-written package would be a layout violation and is
// intentionally not matched.
//
// Use [Pass.IsGenerated] when the path is derived from a *ast.File in
// pass.Files — it calls pass.Rel(f) automatically. Use this function when
// the module-relative path string is already available (e.g. iterating a
// resolver's packages outside a Pass-Driver rule, as in the loader anchor
// test TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3).
//
// Thin delegation to [typeseval.IsGeneratedRelPath].
func IsGeneratedRelPath(rel string) bool {
	return typeseval.IsGeneratedRelPath(rel)
}
