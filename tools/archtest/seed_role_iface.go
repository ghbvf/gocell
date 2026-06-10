package archtest

// seed_role_iface.go — importable SEED-ROLE-IFACE-01 rule logic (#1632 M3).
//
// This is the non-test home of the SEED-ROLE-IFACE-01 scanner so it can be
// compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in a
// _test.go file). GoCell's own TestSEED_ROLE_IFACE_01 in seed_role_iface_test.go
// calls the same CheckSeedRoleIface01 — single source, no parallel rule body.
//
// # SEED-ROLE-IFACE-01
//
// Production code (non *_test.go) must not name the concrete
// `*mem.RoleRepository` type from corecells/accesscore/internal/mem in any
// AST position: function/method signatures, struct fields, type aliases,
// var declarations, or selector expressions of the form `mem.RoleRepository`.
//
// Why: `mem.RoleRepository.SeedRole(role)` is a test-only seed helper that is
// intentionally NOT on the `ports.RoleRepository` interface. Production code
// must depend on the interface (which the PG adapter `PGRoleRepo` implements
// without SeedRole), so that seed paths cannot leak across the cell boundary
// at runtime. Historic violation: `doSeedAdmin` used `(*mem.RoleRepository)`
// type assertion to call SeedRole — that path was removed by the
// `adminprovision.Provisioner` refactor (uses `RoleRepository.Create()`
// instead). This archtest locks the current zero-violation state against
// regression.
//
// AI-robust grade: Hard (violation form uniqueness + RED fixture).
// Picking any shape that names *mem.RoleRepository in production fails
// archtest in CI; there is no Soft string-comment escape.
//
// Blind-spot inventory (not auto-covered by *ast.SelectorExpr walk):
//   - Type aliases `type R = mem.RoleRepository` (and pointer-aliased
//     `type R = *mem.RoleRepository`) — covered by scanForMemRoleRepositoryAliases
//     below, which inspects *ast.TypeSpec with Assign != token.NoPos. RED
//     fixtures: TypeAlias + TypeAliasPointer.
//   - Embedding via interface embedding of mem.RoleRepository — not
//     possible: RoleRepository is a *struct*, not an interface. Compile
//     would fail. No archtest coverage needed.
//   - Dot-imports (`import . "corecells/accesscore/internal/mem"`) — NOT
//     covered by this archtest (memPackageAlias returns "." which is not
//     a valid Go selector base; bare `RoleRepository` Ident would slip
//     past the SelectorExpr scan). Out-of-scope here because GoCell's
//     `LAYER-09` / depguard already bans dot imports on cells/*, and an
//     accesscore-internal dot-import would itself be a layering bug
//     surfaced by other rules.
//   - Internal/mem package itself: skipped (file path contains
//     "/internal/mem/"); in-package code references RoleRepository
//     without a "mem." qualifier, which this archtest does not flag.
//
// Not registered in StandardCellRules: the rule names a gocell-internal package
// path (corecells/accesscore/internal/mem) with no ConfigForExternalCell
// consumer-extension → vacuous-green in an external module; kept importable &
// module-path-agnostic but constrains GoCell's own layout only.
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §S4c T1
// ref: tools/archtest/cells_no_contractspec_import_test.go (form precedent)

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// memPackagePath is the import path of the mem package whose concrete
// *RoleRepository type production code must not name. Derived from
// PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01): no bare literal.
const memPackagePath = PlatformCellsModulePath + "/accesscore/internal/mem"

// CheckSeedRoleIface01 runs SEED-ROLE-IFACE-01 over the running module and
// returns its diagnostics. It scans every production .go file (entire module,
// excluding _test.go, generated, vendor, worktrees, testdata, internal/mem
// itself) and reports any file that imports corecells/accesscore/internal/mem AND
// references mem.RoleRepository in any AST position. It is the importable rule
// body; GoCell's TestSEED_ROLE_IFACE_01 calls it directly — single source, no
// parallel rule body.
func CheckSeedRoleIface01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	scope := scanner.ModuleScope(root) // ModuleScope excludes _test.go by default
	files, err := scope.Files()
	if err != nil {
		t.Fatalf("scanner.ModuleScope.Files: %v", err)
	}
	sort.Strings(files)

	var diags []Diagnostic
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		// Exclude the mem package itself: in-package code references
		// RoleRepository directly without a "mem." qualifier, but a
		// future contributor *could* in theory import the package by its
		// full path from inside the same module subtree. We exempt only
		// the mem directory itself, not "anywhere under accesscore".
		if strings.Contains(rel, "/corecells/accesscore/internal/mem/") {
			continue
		}
		// scanForMemRoleRepositoryUsage already returns structured
		// Diagnostics carrying Rel/Line; Report prepends the rule id, so
		// neither the id nor the location is baked into Message here.
		diags = append(diags, scanForMemRoleRepositoryUsage(token.NewFileSet(), f, rel)...)
	}
	return diags
}

// scanForMemRoleRepositoryUsage returns one structured Diagnostic per violation
// in the file at path when it (a) imports corecells/accesscore/internal/mem and
// (b) references mem.RoleRepository via any selector expression OR type alias.
// Each Diagnostic carries the repo-relative path (Rel) and 1-based Line so
// Report renders an actionable "<rel>:<line>" location; the rule-id prefix is
// added by Report, never baked into Message. Final dedup + ordering is handled
// once by Report's Canonical pass, so this scanner does not deduplicate.
func scanForMemRoleRepositoryUsage(fset *token.FileSet, path, rel string) []Diagnostic {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax errors handled elsewhere
	}

	alias := memPackageAlias(f)
	if alias == "" || alias == "_" {
		return nil // not imported, or blank import (no selector usage)
	}

	var diags []Diagnostic

	// Scan selector expressions: mem.RoleRepository (in any AST position).
	scanner.EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != alias {
			return
		}
		if sel.Sel.Name != "RoleRepository" {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: fset.Position(sel.Pos()).Line,
			Message: fmt.Sprintf(
				"names %s.RoleRepository (concrete mem type) — production code "+
					"must use ports.RoleRepository interface; SeedRole is test-only "+
					"and not on the interface", alias),
		})
	})

	// Scan type alias declarations: `type X = mem.RoleRepository` or
	// `type X = *mem.RoleRepository`. These are *ast.TypeSpec nodes with
	// Assign != token.NoPos; their Type is a SelectorExpr (or a StarExpr
	// wrapping one) whose base Ident matches the mem package alias.
	diags = append(diags, scanForMemRoleRepositoryAliases(fset, f, rel, alias)...)

	return diags
}

// scanForMemRoleRepositoryAliases detects type alias declarations of the form
// `type X = mem.RoleRepository` or `type X = *mem.RoleRepository` (covered by
// the T-ALIAS RED fixture).
//
// AST shape: *ast.TypeSpec with Assign != token.NoPos and Type being either:
//   - *ast.SelectorExpr{X: Ident(alias), Sel: "RoleRepository"}
//   - *ast.StarExpr{X: *ast.SelectorExpr{X: Ident(alias), Sel: "RoleRepository"}}
func scanForMemRoleRepositoryAliases(fset *token.FileSet, f *ast.File, rel, alias string) []Diagnostic {
	var diags []Diagnostic
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Assign == token.NoPos {
			return // not a type alias
		}
		if isMemRoleRepositoryExpr(ts.Type, alias) {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: fset.Position(ts.Pos()).Line,
				Message: fmt.Sprintf(
					"type alias %s = %s.RoleRepository (concrete mem type) — "+
						"production code must use ports.RoleRepository interface",
					ts.Name.Name, alias),
			})
		}
	})
	return diags
}

// isMemRoleRepositoryExpr reports whether expr is `alias.RoleRepository` or
// `*alias.RoleRepository`.
func isMemRoleRepositoryExpr(expr ast.Expr, alias string) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		ident, ok := e.X.(*ast.Ident)
		return ok && ident.Name == alias && e.Sel.Name == "RoleRepository"
	case *ast.StarExpr:
		return isMemRoleRepositoryExpr(e.X, alias)
	}
	return false
}

// memPackageAlias returns the local alias for corecells/accesscore/internal/mem
// in f, or "" when not imported. Handles explicit aliases, default name "mem",
// and the blank "_" import.
func memPackageAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		imported := strings.Trim(imp.Path.Value, `"`)
		if imported != memPackagePath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "mem"
	}
	return ""
}
