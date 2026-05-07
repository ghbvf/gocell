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
// The authoritative definition lives in runtime/auth/roles.go (RoleAdmin).
// Any other file re-declaring or hard-coding the same role string is a drift
// risk: the names will diverge silently when the role value changes.
//
// Detection uses AST-level scanning so string literals inside comments,
// unrelated function calls, and log-field values are not flagged.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
	scope := scanner.DirsScope(root, searchDirsRoleAdmin,
		scanner.ExcludeRels(roleAdminAllowRels...),
	)

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		for _, decl := range fc.File.Decls {
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
					if lit.Value == `"admin"` {
						diags = append(diags, scanner.Diagnostic{
							Rel:  fc.Rel,
							Line: fc.Fset.Position(name.Pos()).Line,
							Message: `duplicate const *Admin* = "admin" violates ` + ruleRoleAdminLiteral01 +
								`; use auth.RoleAdmin from runtime/auth`,
						})
					}
				}
			}
		}
	})
	scanner.Report(t, ruleRoleAdminLiteral01, diags)
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
	scope := scanner.DirsScope(root, searchDirsRoleAdmin)

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		ast.Inspect(fc.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, matched := authCallSiteFuncNames[sel.Sel.Name]; !matched {
				return true
			}
			for _, arg := range call.Args {
				lit, isLit := arg.(*ast.BasicLit)
				if !isLit || lit.Kind != token.STRING {
					continue
				}
				if lit.Value == `"admin"` {
					diags = append(diags, scanner.Diagnostic{
						Rel:  fc.Rel,
						Line: fc.Fset.Position(lit.Pos()).Line,
						Message: `string literal "admin" passed to auth.` + sel.Sel.Name +
							` violates ` + ruleRoleAdminLiteral02 +
							`; use auth.RoleAdmin constant instead`,
					})
				}
			}
			return true
		})
	})
	scanner.Report(t, ruleRoleAdminLiteral02, diags)
}
