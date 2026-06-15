//go:build archtest

// INVARIANT: ROLE-PREFIX-NAMESPACED-01
//
// archtest: ROLE-PREFIX-NAMESPACED-01
//
// # ROLE-PREFIX-NAMESPACED-01 (Medium)
//
// Every exported const whose name starts with "Role" in the business / application
// domain (corecells/ + examples/) MUST satisfy exactly one of:
//
//   - its string value carries the "role:" prefix  (e.g. "role:operator"), OR
//   - its value is a sanctioned platform-reserved bare name: "admin" or "superadmin"
//     (a business cell aliasing the platform role defined in
//     framework/runtime/auth/roles.go).
//
// A const that fails both tests — a bare name like "operator" with no prefix and
// not a platform alias — is a violation.
//
// # Why this rule exists
//
// GoCell has two disjoint role namespaces:
//
//   - Platform-reserved roles — "admin" / "superadmin" — defined once in
//     framework/runtime/auth/roles.go and kept intentionally bare (no prefix).
//     The ROLE-ADMIN-LITERAL-01 rule governs them separately; they are authoritative
//     and immutable from the perspective of business cells.
//
//   - Business / application roles — defined inside individual cells and examples —
//     must carry the "role:" prefix to avoid silent collision with the platform
//     reserved names and to make role semantics self-documenting at the wire layer.
//
// Before this rule, the only guard was a comment in
// examples/iotdevice/cells/devicecell/internal/dto/authz.go (a Soft guard per
// ADR 202606151430-639). This rule upgrades the enforcement to Medium (AST-level
// scan with anti-vacuity + RED fixture), aligned with AI-robust governance charter
// (.claude/rules/gocell/ai-robust.md).
//
// # Scan domain
//
// The scan covers corecells/ (platform cell implementations) and examples/ (example
// cells). The framework/runtime/auth/roles.go authoritative definition file is
// intentionally excluded from the scan domain: it is the definition site for the
// bare platform roles, not a location where convention drift can occur.
//
// # AI-robust rating — Medium (AST literal-scan ceiling)
//
// A Hard guard would require a type-system-enforced funnel (e.g. a sealed Role
// type preventing bare construction). As of ADR 202606151430-639 that Hard path is
// not pursued: the cost of wrapping every role constant in a typed constructor
// exceeds the benefit at pre-GA scale, and the AST scanner catches the realistic
// drift vector (a developer writing a new bare-name const). This is a sanctioned
// Medium standing on its own merits, not a proxy for a reachable Hard guard.
//
// # Blind spots (charter §"强制盲区自检")
//
//   - Runtime-assembled role strings (fmt.Sprintf, concatenation) are not Go
//     compile-time constants and escape the literal scanner.
//   - Role constants whose name does NOT start with "Role" (e.g. "adminRole" or
//     an unexported "roleAdmin") are not matched by the naming pattern and escape
//     the scanner.
//   - Business role constants defined outside the scan domain (corecells/ +
//     examples/) — for instance in cellmodules/ or adapters/ — are not covered.
//   - The allowlist {"admin", "superadmin"} is manually kept in sync with
//     framework/runtime/auth/roles.go. ROLE-ADMIN-LITERAL-01 provides indirect
//     coverage for "admin"; "superadmin" has no separate literal guard (residual
//     blind spot — tracked in ADR 202606151430-639 §3).
//
// # Anti-vacuity
//
// The production scan MUST observe at least [rolePrefixMinKnownRoles] Role*
// string-literal constants to pass. This threshold equals the count of known
// business Role* consts at rule-creation time (2026-06-15):
//
//   - role:operator  (examples/iotdevice)
//   - role:device    (examples/iotdevice)
//   - role:customer  (examples/todoorder)
//   - admin          (examples/iotdevice, platform alias)
//
// If the scan domain drifts (directory renamed, constants renamed away from the
// "Role" prefix) and no longer reaches these definitions, the count falls below the
// threshold and the test fails loud instead of passing vacuously green.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	ruleRolePrefixNamespaced01 = "ROLE-PREFIX-NAMESPACED-01"

	// rolePrefixMinKnownRoles is the anti-vacuity floor: the number of known
	// business Role* string-literal constants in the scan domain as of
	// 2026-06-15 (role:operator, role:device, role:customer, plus the
	// admin alias in iotdevice). Adding new Role* consts to corecells/ or
	// examples/ will keep the count above this floor; removing / renaming
	// enough consts to drop below it is the scanner drift signal this guard
	// is designed to detect.
	rolePrefixMinKnownRoles = 4
)

// rolePrefixPlatformAllowlist is the set of bare role values that a business
// cell is permitted to use when aliasing a platform-reserved role. These
// values are sourced from framework/runtime/auth/roles.go (RoleAdmin /
// RoleSuperAdmin) and kept in sync here manually.
//
// Blind spot: if roles.go gains a new reserved name it must also be added
// here; there is no compile-time enforcement of that sync (residual Medium
// ceiling documented in ADR 202606151430-639 §3).
var rolePrefixPlatformAllowlist = map[string]struct{}{
	"admin":      {},
	"superadmin": {},
}

// TestRolePrefixNamespaced01 enforces ROLE-PREFIX-NAMESPACED-01.
//
// It walks production .go files under corecells/ and examples/, scanning
// top-level const declarations for any exported identifier whose name
// starts with "Role" and whose value is a string literal. For each such
// constant it asserts:
//
//   - strings.HasPrefix(value, "role:"), OR
//   - value ∈ {"admin", "superadmin"} (sanctioned platform role aliases).
//
// Any constant that fails both checks is reported as a violation.
//
// The anti-vacuity guard below asserts that ≥ [rolePrefixMinKnownRoles]
// Role* string-literal constants were seen during the scan.
func TestRolePrefixNamespaced01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, platformAndExampleCellScanDirs())

	var seenCount int
	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.GenDecl](f, func(genDecl *ast.GenDecl) {
				if genDecl.Tok != token.CONST {
					return
				}

				var lastValues []ast.Expr
				EachInChildren[ast.ValueSpec](genDecl, func(vs *ast.ValueSpec) {
					values := vs.Values
					if values == nil {
						values = lastValues
					} else {
						lastValues = values
					}
					for i, name := range vs.Names {
						if !isRoleIdent(name.Name) {
							continue
						}
						if i >= len(values) {
							continue
						}
						lit, ok := values[i].(*ast.BasicLit)
						if !ok {
							continue
						}
						v, ok := StringLitValue(lit)
						if !ok {
							continue
						}
						seenCount++
						if isValidBusinessRole(v) {
							continue
						}
						out = append(out, Diagnostic{
							Rel:  p.Rel(f),
							Line: p.Fset.Position(name.Pos()).Line,
							Message: ruleRolePrefixNamespaced01 + `: business role const must be ` +
								`"role:"-namespaced or alias a reserved platform role ` +
								`(admin/superadmin); got ` + v,
						})
					}
				})
			})
		}
		return out
	})

	// Anti-vacuity: the scan must have reached enough known Role* consts.
	// A count of 0 (or below floor) means the scan domain or naming pattern
	// drifted and the rule is silently vacuous.
	if seenCount < rolePrefixMinKnownRoles {
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"%s anti-vacuity: only %d Role* string-literal const(s) observed in "+
					"corecells/ + examples/ — expected at least %d. "+
					"The scan domain or Role* naming pattern may have drifted "+
					"(directories renamed, consts renamed away from the 'Role' prefix).",
				ruleRolePrefixNamespaced01, seenCount, rolePrefixMinKnownRoles,
			),
		})
	}

	Report(t, ruleRolePrefixNamespaced01, diags)
}

// TestRolePrefixNamespaced01_RedFixture is the negative control: the scanner
// run against roleprefixfixture must fire on exactly the one RED case
// (RoleBad = "operator") and leave the four GREEN cases untouched.
func TestRolePrefixNamespaced01_RedFixture(t *testing.T) {
	t.Parallel()

	fixturePkg := "./tools/archtest/internal/roleprefixfixture"

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				EachInSubtree[ast.GenDecl](f, func(genDecl *ast.GenDecl) {
					if genDecl.Tok != token.CONST {
						return
					}
					var lastValues []ast.Expr
					EachInChildren[ast.ValueSpec](genDecl, func(vs *ast.ValueSpec) {
						values := vs.Values
						if values == nil {
							values = lastValues
						} else {
							lastValues = values
						}
						for i, name := range vs.Names {
							if !isRoleIdent(name.Name) {
								continue
							}
							if i >= len(values) {
								continue
							}
							lit, ok := values[i].(*ast.BasicLit)
							if !ok {
								continue
							}
							v, ok := StringLitValue(lit)
							if !ok {
								continue
							}
							if !isValidBusinessRole(v) {
								found++
							}
						}
					})
				})
			}
			return nil
		})

	assert.Equal(t, 1, found,
		"ROLE-PREFIX-NAMESPACED-01 RED fixture self-check FAILED: expected exactly 1 "+
			"violation (RoleBad = \"operator\"). Got %d — "+
			"found<1 means the scanner missed the bare-name RED case; "+
			"found>1 means it over-matched a GREEN case (role:-prefixed or platform alias).", found)
}

// isRoleIdent reports whether name is an exported identifier that starts
// with "Role". The scan only flags identifiers that match this pattern;
// role constants with non-"Role" names are a documented blind spot.
func isRoleIdent(name string) bool {
	return strings.HasPrefix(name, "Role")
}

// isValidBusinessRole reports whether v satisfies the naming convention:
// either a "role:"-prefixed business role, or a sanctioned platform bare name.
func isValidBusinessRole(v string) bool {
	if strings.HasPrefix(v, "role:") {
		return true
	}
	_, ok := rolePrefixPlatformAllowlist[v]
	return ok
}
