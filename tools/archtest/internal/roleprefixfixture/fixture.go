//go:build archtest_fixture

// Package roleprefixfixture is the RED/GREEN fixture for
// ROLE-PREFIX-NAMESPACED-01. It proves the AST scanner fires on bare-name
// business role constants and passes through correctly namespaced or
// sanctioned platform-alias values.
//
// RED (must be flagged — exactly 2):
//   - RoleBad      = "operator"  — bare name, not "role:"-prefixed and not a
//     sanctioned platform value → violation.
//   - RoleOperator = "admin"     — carries a sanctioned bare platform VALUE
//     but uses a non-canonical identifier name; a business cell writing
//     RoleOperator = "admin" would silently acquire platform-admin semantics
//     (privilege escalation false-negative closed by F1 of PR #2214).
//
// GREEN (must NOT be flagged):
//   - RoleGood      = "role:operator"  — correctly namespaced business role.
//   - RoleAdmin     = "admin"          — canonical alias: name "RoleAdmin" +
//     value "admin" matches the rolePrefixPlatformAlias entry exactly.
//   - RoleSuperAdmin = "superadmin"    — canonical alias: name "RoleSuperAdmin"
//     + value "superadmin" matches the rolePrefixPlatformAlias entry exactly.
//   - notARole = "bare"               — unexported, does not start with "Role";
//     ignored by the Role* naming filter.
package roleprefixfixture

// --- RED: must be caught by ROLE-PREFIX-NAMESPACED-01 ---

// RoleBad is a business role constant with a bare name that lacks the
// required "role:" prefix. It is not a sanctioned platform value either.
const RoleBad = "operator"

// RoleOperator carries the sanctioned platform bare value "admin" but under a
// non-canonical identifier name. Under the old value-only allowlist this was a
// false-negative (silently passed). Under the name↔value binding introduced by
// F1, it is correctly flagged: only the canonical name "RoleAdmin" is permitted
// to carry the value "admin".
const RoleOperator = "admin"

// --- GREEN: must NOT be caught ---

// RoleGood is correctly namespaced with the "role:" prefix.
const RoleGood = "role:operator"

// RoleAdmin aliases the platform-reserved admin role using the canonical
// identifier name "RoleAdmin" paired with the sanctioned bare value "admin".
// Both name and value must match the rolePrefixPlatformAlias entry for this
// to be accepted.
const RoleAdmin = "admin"

// RoleSuperAdmin aliases the platform-reserved superadmin role using the
// canonical identifier name "RoleSuperAdmin" paired with the sanctioned bare
// value "superadmin".
const RoleSuperAdmin = "superadmin"

// notARole does NOT start with "Role", so the scanner's naming filter excludes it.
// Its bare value must not be flagged.
const notARole = "bare"
