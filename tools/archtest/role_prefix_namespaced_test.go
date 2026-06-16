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
//   - it is a canonical platform-alias: the identifier name AND value together
//     match an entry in rolePrefixPlatformAlias (e.g. name "RoleAdmin" with
//     value "admin", or name "RoleSuperAdmin" with value "superadmin").
//
// A const that fails both tests is a violation.  This includes the privilege-
// escalation vector: a business cell writing `RoleOperator = "admin"` carries
// a sanctioned platform value under a non-canonical name, which would silently
// grant platform-admin semantics.  The name↔value binding closes that false-
// negative (PR #2214 F1).
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
//   - The alias map {RoleAdmin→"admin", RoleSuperAdmin→"superadmin"} is manually
//     kept in sync with framework/runtime/auth/roles.go. ROLE-ADMIN-LITERAL-01
//     provides indirect coverage for "admin"; "superadmin" has no separate literal
//     guard (residual blind spot — this godoc is the authoritative blind-spot
//     record; ADR 202606151430-639 §3.1 summarizes enforcement).
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
//   - admin          (examples/iotdevice, canonical alias RoleAdmin)
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
	"github.com/stretchr/testify/require"
)

const (
	ruleRolePrefixNamespaced01 = "ROLE-PREFIX-NAMESPACED-01"

	// rolePrefixMinKnownRoles is the anti-vacuity floor: the number of known
	// business Role* string-literal constants in the scan domain as of
	// 2026-06-15 (role:operator, role:device, role:customer, plus the
	// canonical alias RoleAdmin="admin" in iotdevice). Adding new Role* consts
	// to corecells/ or examples/ will keep the count above this floor;
	// removing / renaming enough consts to drop below it is the scanner drift
	// signal this guard is designed to detect.
	rolePrefixMinKnownRoles = 4
)

// rolePrefixPlatformAlias maps the canonical constant identifier name to its
// sanctioned bare platform value. Only when BOTH name AND value match an entry
// here is the const accepted as a legitimate platform-role alias.
//
// This replaces the former value-only rolePrefixPlatformAllowlist (which
// accepted any identifier named "Role*" carrying "admin", enabling the
// privilege-escalation false-negative `RoleOperator = "admin"`).
//
// Blind spot: if roles.go gains a new reserved name, it must be added here
// together with its canonical identifier name; there is no compile-time
// enforcement of that sync (residual Medium ceiling documented in ADR
// 202606151430-639 §3.1).
var rolePrefixPlatformAlias = map[string]string{
	"RoleAdmin":      "admin",
	"RoleSuperAdmin": "superadmin",
}

// rolePrefixDiagnostics is the shared scanning core used by both the
// production test and the RED fixture self-check.  Separating the scan logic
// from the test runner ensures that the fixture exercises the same code path
// as production — a previously independent re-implementation in the fixture
// could pass while the production path was broken (F2, PR #2214).
//
// It returns every Diagnostic produced by the scan plus the count of Role*
// string-literal constants observed (used for anti-vacuity in production).
func rolePrefixDiagnostics(p *Pass) (diags []Diagnostic, seen int) {
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
					seen++
					if isValidBusinessRole(name.Name, v) {
						continue
					}
					diags = append(diags, Diagnostic{
						Rel:  p.Rel(f),
						Line: p.Fset.Position(name.Pos()).Line,
						Message: ruleRolePrefixNamespaced01 + `: business role const must be ` +
							`"role:"-namespaced or use a canonical platform alias ` +
							`(RoleAdmin="admin" / RoleSuperAdmin="superadmin"); ` +
							`got ` + name.Name + ` = ` + v,
					})
				}
			})
		})
	}
	return diags, seen
}

// TestRolePrefixNamespaced01 enforces ROLE-PREFIX-NAMESPACED-01.
//
// It walks production .go files under corecells/ and examples/, scanning
// top-level const declarations for any exported identifier whose name
// starts with "Role" and whose value is a string literal. For each such
// constant it asserts:
//
//   - strings.HasPrefix(value, "role:"), OR
//   - name AND value together match a rolePrefixPlatformAlias entry
//     (e.g. name="RoleAdmin", value="admin").
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
		d, s := rolePrefixDiagnostics(p)
		seenCount += s
		return d
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
// run against roleprefixfixture must fire on exactly the two RED cases
// (RoleBad = "operator" and RoleOperator = "admin") and leave the three GREEN
// Role* cases (RoleGood / RoleAdmin / RoleSuperAdmin) untouched; the non-Role*
// notARole is ignored by the naming filter.
//
// The fixture is scanned via the shared rolePrefixDiagnostics function (same
// code path as production) — not via an independent re-implementation — so a
// regression in the production path is also caught here (F2, PR #2214).
func TestRolePrefixNamespaced01_RedFixture(t *testing.T) {
	t.Parallel()

	fixturePkg := "./tools/archtest/internal/roleprefixfixture"

	var allDiags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{fixturePkg}),
		func(p *Pass) []Diagnostic {
			d, _ := rolePrefixDiagnostics(p)
			allDiags = append(allDiags, d...)
			return nil
		})

	require.Len(t, allDiags, 2,
		"ROLE-PREFIX-NAMESPACED-01 RED fixture self-check FAILED: expected exactly 2 "+
			"violations (RoleBad=\"operator\" and RoleOperator=\"admin\"). Got %d — "+
			"<2 means the scanner missed a RED case; >2 means it over-matched a GREEN case.",
		len(allDiags))

	// Verify each diagnostic targets the expected constant.
	assert.Contains(t, allDiags[0].Message+allDiags[1].Message, "RoleBad",
		"expected one diagnostic to mention RoleBad")
	assert.Contains(t, allDiags[0].Message+allDiags[1].Message, "RoleOperator",
		"expected one diagnostic to mention RoleOperator")
}

// isRoleIdent reports whether name is an exported identifier that starts
// with "Role". The scan only flags identifiers that match this pattern;
// role constants with non-"Role" names are a documented blind spot.
func isRoleIdent(name string) bool {
	return strings.HasPrefix(name, "Role")
}

// isValidBusinessRole reports whether the constant with the given identifier
// name and string value satisfies the naming convention:
//
//   - the value carries a "role:" prefix (business role), OR
//   - name AND value together match a canonical platform-alias entry in
//     rolePrefixPlatformAlias (e.g. name="RoleAdmin", value="admin").
//
// The second condition binds both name and value, preventing the privilege-
// escalation false-negative where any arbitrary name (e.g. "RoleOperator")
// could carry a sanctioned platform value ("admin") and be silently accepted.
func isValidBusinessRole(name, value string) bool {
	if strings.HasPrefix(value, "role:") {
		return true
	}
	want, ok := rolePrefixPlatformAlias[name]
	return ok && want == value
}
