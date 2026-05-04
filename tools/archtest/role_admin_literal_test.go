// archtest: ROLE-ADMIN-LITERAL-01
// archtest: ROLE-ADMIN-LITERAL-02
//
// ROLE-ADMIN-LITERAL-01 forbids duplicate const declarations of the form
//
//	const <name containing "Admin"> = "admin"
//
// across runtime/, cells/, adapters/, and cmd/.
//
// ROLE-ADMIN-LITERAL-02 forbids passing the string literal "admin" as an
// argument to auth.AnyRole / auth.SelfOr / auth.RequireAnyRole / auth.AnyRoles.
// All call sites must use the auth.RoleAdmin constant instead.
//
// The authoritative definition lives in runtime/auth/roles.go (RoleAdmin).
// Any other file re-declaring or hard-coding the same role string is a drift
// risk: the names will diverge silently when the role value changes.
//
// Detection uses AST-level scanning so string literals inside comments,
// unrelated function calls, and log-field values are not flagged.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleRoleAdminLiteral01 = "ROLE-ADMIN-LITERAL-01"
	ruleRoleAdminLiteral02 = "ROLE-ADMIN-LITERAL-02"
)

// roleAdminAllowlist contains the files that are explicitly permitted to declare
// a const whose name contains "Admin"/"admin" with the value "admin".
//
//   - runtime/auth/roles.go: canonical definition of RoleAdmin.
//   - runtime/http/devtools/catalog.go: local copy (roleAdmin) kept in sync
//     until runtime/ gains an internal-only reference to runtime/auth.
//   - cells/accesscore/initialadmin/bootstrap.go: defaultAdminUsername is the
//     default account username at provisioning time, not a role name.
var roleAdminAllowlist = map[string]struct{}{
	"runtime/auth/roles.go":                      {},
	"runtime/http/devtools/catalog.go":           {},
	"cells/accesscore/initialadmin/bootstrap.go": {},
}

// TestRoleAdminLiteralIsForbidden enforces ROLE-ADMIN-LITERAL-01.
//
// It walks production .go files under runtime/, cells/, adapters/, and cmd/,
// scanning top-level const declarations for any identifier whose name
// contains "Admin" or "admin" and whose value is the string literal "admin".
// The only allowed file is the canonical definition in runtime/auth/roles.go.
func TestRoleAdminLiteralIsForbidden(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "adapters"),
		filepath.Join(root, "cmd"),
	}

	var violations []string

	for _, dir := range searchDirs {
		files, err := findProductionGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		require.NoErrorf(t, err, "reading dir %s", dir)

		for _, f := range files {
			rel, relErr := filepath.Rel(root, f)
			if relErr != nil {
				rel = f
			}
			rel = filepath.ToSlash(rel)
			if _, allowed := roleAdminAllowlist[rel]; allowed {
				continue
			}

			hits, err := findRoleAdminConstLiterals(f)
			require.NoErrorf(t, err, "scanning %s", f)

			for _, line := range hits {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: duplicate const *Admin* = \"admin\" violates %s; use auth.RoleAdmin from runtime/auth",
					rel, line, ruleRoleAdminLiteral01))
			}
		}
	}

	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"%s: const declarations of `*Admin* = \"admin\"` are only allowed in the allowlisted files; "+
			"all other packages must reference auth.RoleAdmin from runtime/auth.",
		ruleRoleAdminLiteral01)
}

// findRoleAdminConstLiterals parses path and returns line numbers of every
// top-level const declaration whose identifier name contains "admin" or
// "Admin" and whose value is the string literal "admin".
func findRoleAdminConstLiterals(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		// Ignore parse errors for files that only compile under specific build tags.
		return nil, nil //nolint:nilerr // parse errors indicate files that only compile under specific build tags; safe to skip
	}
	var lines []int
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !isAdminIdent(name.Name) {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				// BasicLit.Value includes surrounding quotes: `"admin"`.
				if lit.Value == `"admin"` {
					lines = append(lines, fset.Position(name.Pos()).Line)
				}
			}
		}
	}
	return lines, nil
}

// isAdminIdent reports whether name is a role-admin style identifier:
// it must contain the substring "Admin" or (case-insensitively) "admin"
// as a component, not just as a prefix of a longer word unrelated to roles.
// We use a simple strings.Contains which is intentionally broad — any
// admin-named constant in the scanned packages that holds "admin" is suspect.
func isAdminIdent(name string) bool {
	return strings.Contains(name, "Admin") || strings.Contains(strings.ToLower(name), "admin")
}

// authCallSiteFuncNames is the set of auth.*(...) function names whose
// arguments must not contain the string literal "admin". All call sites
// must use auth.RoleAdmin instead.
var authCallSiteFuncNames = map[string]struct{}{
	"AnyRole":        {},
	"AnyRoles":       {},
	"SelfOr":         {},
	"RequireAnyRole": {},
}

// TestRoleAdminCallSiteLiteralIsForbidden enforces ROLE-ADMIN-LITERAL-02.
//
// It walks production .go files under runtime/, cells/, adapters/, and cmd/
// (excluding *_test.go), scanning call expressions of the form
// auth.AnyRole(...) / auth.AnyRoles(...) / auth.SelfOr(...) /
// auth.RequireAnyRole(...). Any argument that is the bare string literal
// "admin" triggers a violation — callers must use the auth.RoleAdmin constant
// so that role-string changes propagate atomically from the single definition
// in runtime/auth/roles.go.
func TestRoleAdminCallSiteLiteralIsForbidden(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "adapters"),
		filepath.Join(root, "cmd"),
	}

	var violations []string

	for _, dir := range searchDirs {
		files, err := findProductionGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		require.NoErrorf(t, err, "reading dir %s", dir)

		for _, f := range files {
			rel, relErr := filepath.Rel(root, f)
			if relErr != nil {
				rel = f
			}
			rel = filepath.ToSlash(rel)

			hits, scanErr := findAdminLiteralCallSites(f)
			require.NoErrorf(t, scanErr, "scanning %s", f)

			for _, line := range hits {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: string literal \"admin\" passed to auth.* call violates %s; use auth.RoleAdmin constant instead",
					rel, line, ruleRoleAdminLiteral02))
			}
		}
	}

	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"%s: string literal \"admin\" must not appear as an argument to auth.AnyRole / auth.AnyRoles / "+
			"auth.SelfOr / auth.RequireAnyRole; use auth.RoleAdmin from runtime/auth/roles.go.",
		ruleRoleAdminLiteral02)
}

// findAdminLiteralCallSites parses path and returns line numbers of every
// call expression of the form auth.<FuncName>(...) where any argument is the
// string literal "admin". Test files (*_test.go) are excluded by
// findProductionGoFilesInDir before this function is called.
func findAdminLiteralCallSites(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		// Ignore parse errors for files that only compile under specific build tags.
		return nil, nil //nolint:nilerr // parse errors indicate files that only compile under specific build tags; safe to skip
	}

	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Match calls whose selector name is in authCallSiteFuncNames.
		if _, matched := authCallSiteFuncNames[sel.Sel.Name]; !matched {
			return true
		}
		// Scan each argument for a bare "admin" string literal.
		for _, arg := range call.Args {
			lit, isLit := arg.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				continue
			}
			if lit.Value == `"admin"` {
				lines = append(lines, fset.Position(lit.Pos()).Line)
			}
		}
		return true
	})
	return lines, nil
}
