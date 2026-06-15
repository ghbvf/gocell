//go:build archtest_fixture

// Package roleprefixfixture is the RED/GREEN fixture for
// ROLE-PREFIX-NAMESPACED-01. It proves the AST scanner fires on bare-name
// business role constants and passes through correctly namespaced or
// sanctioned platform-alias values.
//
// RED (must be flagged — exactly 1):
//   - RoleBad = "operator"  — bare name, not "role:"-prefixed and not a
//     sanctioned platform value → violation.
//
// GREEN (must NOT be flagged):
//   - RoleGood      = "role:operator"  — correctly namespaced business role.
//   - RoleAdminAlias = "admin"         — sanctioned platform role alias.
//   - RoleSuperAdmin  = "superadmin"   — sanctioned platform role alias.
//   - notARole = "bare"               — unexported, does not start with "Role";
//     ignored by the Role* naming filter.
package roleprefixfixture

// --- RED: must be caught by ROLE-PREFIX-NAMESPACED-01 ---

// RoleBad is a business role constant with a bare name that lacks the
// required "role:" prefix. It is not a sanctioned platform value either.
const RoleBad = "operator"

// --- GREEN: must NOT be caught ---

// RoleGood is correctly namespaced with the "role:" prefix.
const RoleGood = "role:operator"

// RoleAdminAlias aliases the platform-reserved admin role (sanctioned bare value).
const RoleAdminAlias = "admin"

// RoleSuperAdmin aliases the platform-reserved superadmin role (sanctioned bare value).
const RoleSuperAdmin = "superadmin"

// notARole does NOT start with "Role", so the scanner's naming filter excludes it.
// Its bare value must not be flagged.
const notARole = "bare"
