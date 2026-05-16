// INVARIANT: ROLE-ADMIN-LITERAL-01
// INVARIANT: ROLE-ADMIN-LITERAL-02
//
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
// The call-site rule is import-aware: the receiver of the selector
// expression must resolve to a local alias of github.com/ghbvf/gocell/runtime/auth
// (default name "auth" or any explicit rename via `import x "…/runtime/auth"`).
// A same-named method on an unrelated type or package does NOT trigger the
// rule. Literal comparison is normalized through scanner.StringLitValue, so
// raw-string forms (“ `admin` “) and escape-encoded forms (`"\x61dmin"`)
// are caught alongside the plain `"admin"` form.
//
// The authoritative definition lives in runtime/auth/roles.go (RoleAdmin).
// Any other file re-declaring or hard-coding the same role string is a drift
// risk: the names will diverge silently when the role value changes.
//
// Detection uses AST-level scanning so string literals inside comments,
// unrelated function calls, and log-field values are not flagged.
package archtest

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

const (
	ruleRoleAdminLiteral01 = "ROLE-ADMIN-LITERAL-01"
	ruleRoleAdminLiteral02 = "ROLE-ADMIN-LITERAL-02"
)

// roleAdminAllowRels contains the files that are explicitly permitted to declare
// a const whose name contains "Admin"/"admin" with the value "admin".
//
//   - runtime/auth/roles.go: canonical definition of RoleAdmin.
//   - runtime/http/devtools/catalog.go: local copy (roleAdmin) kept in sync
//     until runtime/ gains an internal-only reference to runtime/auth.
//   - cells/accesscore/initialadmin/bootstrap.go: defaultAdminUsername is the
//     default account username at provisioning time, not a role name.
var roleAdminAllowRels = []string{
	"runtime/auth/roles.go",
	"runtime/http/devtools/catalog.go",
	"cells/accesscore/initialadmin/bootstrap.go",
}

// searchDirsRoleAdmin are the directories scanned by both ROLE-ADMIN-LITERAL rules.
var searchDirsRoleAdmin = []string{"runtime", "cells", "adapters", "cmd"}

// TestRoleAdminLiteralIsForbidden enforces ROLE-ADMIN-LITERAL-01.
//
// It walks production .go files under runtime/, cells/, adapters/, and cmd/,
// scanning top-level const declarations for any identifier whose name
// contains "Admin" or "admin" and whose value is the string literal "admin".
// The only allowed files are listed in roleAdminAllowRels.
func TestRoleAdminLiteralIsForbidden(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, searchDirsRoleAdmin,
		ExcludeRels(roleAdminAllowRels...),
	)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.GenDecl](f, func(genDecl *ast.GenDecl) {
				if genDecl.Tok != token.CONST {
					return
				}
				// Go spec: a ValueSpec inside a const GenDecl with no Values
				// inherits the previous spec's expression list (iota carry).
				// Track the most recent non-empty Values within this GenDecl so
				// that `const ( AdminRole = "admin"; OtherRole )` flags OtherRole.
				var lastValues []ast.Expr
				EachInChildren[ast.ValueSpec](genDecl, func(vs *ast.ValueSpec) {
					values := vs.Values
					if values == nil {
						values = lastValues
					} else {
						lastValues = values
					}
					for i, name := range vs.Names {
						if !isAdminIdent(name.Name) {
							continue
						}
						if i >= len(values) {
							continue
						}
						lit, ok := values[i].(*ast.BasicLit)
						if !ok {
							continue
						}
						value, ok := StringLitValue(lit)
						if !ok || value != "admin" {
							continue
						}
						out = append(out, Diagnostic{
							Rel:  p.Rel(f),
							Line: p.Fset.Position(name.Pos()).Line,
							Message: `duplicate const *Admin* = "admin" violates ` + ruleRoleAdminLiteral01 +
								`; use auth.RoleAdmin from runtime/auth`,
						})
					}
				})
			})
		}
		return out
	})
	Report(t, ruleRoleAdminLiteral01, diags)
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
//
// Type-aware via typeseval.SharedResolver + go/types Info — closes the
// PackageAliases-based detection limit (PR445-FU-PACKAGEALIASES-TYPE-AWARE-01
// for this caller; the const-scan rule above remains AST-only because it
// does not depend on import resolution).
func TestRoleAdminCallSiteLiteralIsForbidden(t *testing.T) {
	t.Parallel()

	// tests=false matches the original DirsScope(searchDirsRoleAdmin) which
	// excluded *_test.go by default.
	diags := RunTyped(t, TypedOpts{}, []string{
		"./runtime/...", "./cells/...", "./adapters/...", "./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			// ResolvePackageRef accepts both *ast.SelectorExpr (path A.2
			// qualified `auth.Func(...)`) and *ast.Ident (path A.3 dot-imported
			// bare `Func(...)` after `import . ".../runtime/auth"`); closes the
			// dot-import bypass that the prior PkgName-only matcher missed.
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				path, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok || path != authRuntimeImportPath {
					return
				}
				if _, matched := authCallSiteFuncNames[name]; !matched {
					return
				}
				EachInSubtree[ast.BasicLit](call, func(lit *ast.BasicLit) {
					value, ok := StringLitValue(lit)
					if !ok || value != "admin" {
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: `string literal "admin" passed to auth.` + name +
							` violates ` + ruleRoleAdminLiteral02 +
							`; use auth.RoleAdmin constant instead`,
					})
				})
			})
		}
		return out
	})
	Report(t, ruleRoleAdminLiteral02, diags)
}
