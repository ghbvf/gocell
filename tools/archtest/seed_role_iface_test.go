// INVARIANT: SEED-ROLE-IFACE-01
//
// # SEED-ROLE-IFACE-01
//
// Production code (non *_test.go) must not name the concrete
// `*mem.RoleRepository` type from cells/accesscore/internal/mem in any
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
// AI-rebust grade: Hard (violation form uniqueness + RED fixture).
// Picking any shape that names *mem.RoleRepository in production fails
// archtest in CI; there is no Soft string-comment escape.
//
// Blind-spot inventory (not auto-covered by *ast.SelectorExpr walk):
//   - Type aliases `type R = mem.RoleRepository` — covered by separate
//     scan for *ast.TypeSpec with Assign != token.NoPos + RHS selector
//     (see scanForMemRoleRepositoryAliases below). RED fixture T-ALIAS.
//   - Embedding via interface embedding of mem.RoleRepository — not
//     possible: RoleRepository is a *struct*, not an interface. Compile
//     would fail. No archtest coverage needed.
//   - Dot-imports (`import . "cells/accesscore/internal/mem"`) — covered
//     by detecting alias == "." and scanning bare `RoleRepository` Ident.
//     Out-of-scope here: GoCell has `LAYER-09` / depguard banning dot
//     imports on cells/* already. Documented out of this archtest's scope.
//   - Internal/mem package itself: skipped (file path contains
//     "/internal/mem/").
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §S4c T1
// ref: tools/archtest/cells_no_contractspec_import_test.go (form precedent)
package archtest

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

// memPackagePath is the absolute import path of the mem package whose
// concrete *RoleRepository type production code must not name.
const memPackagePath = "github.com/ghbvf/gocell/cells/accesscore/internal/mem"

// TestSEED_ROLE_IFACE_01 scans every production .go file (entire module,
// excluding _test.go, generated, vendor, worktrees, testdata, internal/mem
// itself) and fails if any file imports cells/accesscore/internal/mem AND
// references mem.RoleRepository in any AST position.
func TestSEED_ROLE_IFACE_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.ModuleScope(root) // ModuleScope excludes _test.go by default
	files, err := scope.Files()
	if err != nil {
		t.Fatalf("scanner.ModuleScope.Files: %v", err)
	}
	sort.Strings(files)

	var violations []string
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		// Exclude the mem package itself: in-package code references
		// RoleRepository directly without a "mem." qualifier, but a
		// future contributor *could* in theory import the package by its
		// full path from inside the same module subtree. We exempt only
		// the mem directory itself, not "anywhere under accesscore".
		if strings.Contains(rel, "/cells/accesscore/internal/mem/") {
			continue
		}
		hits := scanForMemRoleRepositoryUsage(token.NewFileSet(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("SEED-ROLE-IFACE-01: %s", v)
	}
}

// scanForMemRoleRepositoryUsage returns violation strings for the file at
// path when it (a) imports cells/accesscore/internal/mem and (b) references
// mem.RoleRepository via any selector expression OR type alias.
func scanForMemRoleRepositoryUsage(fset *token.FileSet, path, rel string) []string {
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

	var violations []string

	// Scan selector expressions: mem.RoleRepository (in any AST position).
	scanner.EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != alias {
			return
		}
		if sel.Sel.Name != "RoleRepository" {
			return
		}
		pos := fset.Position(sel.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: names %s.RoleRepository (concrete mem type) — production code "+
				"must use ports.RoleRepository interface; SeedRole is test-only and "+
				"not on the interface",
			rel, pos.Line, alias,
		))
	})

	// Scan type alias declarations: `type X = mem.RoleRepository` or
	// `type X = *mem.RoleRepository`. These are *ast.TypeSpec nodes with
	// Assign != token.NoPos; their Type is a SelectorExpr (or a StarExpr
	// wrapping one) whose base Ident matches the mem package alias.
	violations = append(violations, scanForMemRoleRepositoryAliases(fset, f, rel, alias)...)

	// Deduplicate.
	seen := make(map[string]bool, len(violations))
	out := violations[:0]
	for _, v := range violations {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// scanForMemRoleRepositoryAliases detects type alias declarations of the form
// `type X = mem.RoleRepository` or `type X = *mem.RoleRepository` (covered by
// the T-ALIAS RED fixture).
//
// AST shape: *ast.TypeSpec with Assign != token.NoPos and Type being either:
//   - *ast.SelectorExpr{X: Ident(alias), Sel: "RoleRepository"}
//   - *ast.StarExpr{X: *ast.SelectorExpr{X: Ident(alias), Sel: "RoleRepository"}}
func scanForMemRoleRepositoryAliases(fset *token.FileSet, f *ast.File, rel, alias string) []string {
	var violations []string
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Assign == token.NoPos {
			return // not a type alias
		}
		if isMemRoleRepositoryExpr(ts.Type, alias) {
			pos := fset.Position(ts.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: type alias %s = %s.RoleRepository (concrete mem type) — "+
					"production code must use ports.RoleRepository interface",
				rel, pos.Line, ts.Name.Name, alias,
			))
		}
	})
	return violations
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

// memPackageAlias returns the local alias for cells/accesscore/internal/mem
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

// TestSEED_ROLE_IFACE_01_RedFixture_FuncParam verifies the scanner correctly
// flags a function with parameter type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_FuncParam(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
func badParam(repo *mem.RoleRepository) {}
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (func param *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_StructField verifies the scanner flags
// a struct field of type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_StructField(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
type Bad struct {
	Repo *mem.RoleRepository
}
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (struct field *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_VarDecl verifies the scanner flags a
// var declaration with explicit type *mem.RoleRepository.
func TestSEED_ROLE_IFACE_01_RedFixture_VarDecl(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
var bad *mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (var *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_AliasedImport verifies the scanner flags
// the violation even when the package is imported under an alias.
func TestSEED_ROLE_IFACE_01_RedFixture_AliasedImport(t *testing.T) {
	t.Parallel()
	src := `package p
import accessmem "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
type Bad struct {
	R *accessmem.RoleRepository
}
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/cell.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (aliased import) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_GreenFixture_NoImport verifies the scanner does NOT
// flag a file that does not import the mem package even if "RoleRepository"
// appears as text (e.g., in a comment or as a bare Ident from another type).
func TestSEED_ROLE_IFACE_01_GreenFixture_NoImport(t *testing.T) {
	t.Parallel()
	src := `package p
// This comment mentions mem.RoleRepository as documentation only.
type Other struct {
	Name string
}
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/cell.go")
	if len(violations) != 0 {
		t.Errorf("GREEN fixture (no mem import) must produce 0 violations; got %d: %v",
			len(violations), violations)
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_TypeAlias verifies the scanner flags a type
// alias declaration `type LocalRoleRepo = mem.RoleRepository` (T-ALIAS RED fixture
// from the blind-spot inventory).
func TestSEED_ROLE_IFACE_01_RedFixture_TypeAlias(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
type LocalRoleRepo = mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (type alias mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_RedFixture_TypeAliasPointer verifies the scanner flags
// `type LocalRoleRepo = *mem.RoleRepository` (StarExpr-wrapped alias form).
func TestSEED_ROLE_IFACE_01_RedFixture_TypeAliasPointer(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
type LocalRoleRepo = *mem.RoleRepository
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/some_prod.go")
	if len(violations) == 0 {
		t.Errorf("RED fixture (type alias *mem.RoleRepository) must produce a violation; got 0")
	}
}

// TestSEED_ROLE_IFACE_01_GreenFixture_BlankImport verifies a blank import
// (side-effect only) of mem produces no violation; the blank alias cannot
// be used as a selector qualifier.
func TestSEED_ROLE_IFACE_01_GreenFixture_BlankImport(t *testing.T) {
	t.Parallel()
	src := `package p
import _ "github.com/ghbvf/gocell/cells/accesscore/internal/mem"
`
	violations := scanSrcForViolations(t, src, "cells/accesscore/cell.go")
	if len(violations) != 0 {
		t.Errorf("GREEN fixture (blank import) must produce 0 violations; got %d: %v",
			len(violations), violations)
	}
}

// scanSrcForViolations is a test helper that writes src to a temp file and
// runs scanForMemRoleRepositoryUsage on it with the given relative path.
func scanSrcForViolations(t *testing.T, src, rel string) []string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "seed_role_iface_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	return scanForMemRoleRepositoryUsage(token.NewFileSet(), tmp.Name(), rel)
}
