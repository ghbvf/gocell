package auth

// RoleAdmin and RoleSuperAdmin are the platform-reserved role constants. Their
// values are the single source of truth for every role-string comparison
// (ABAC baseline conditions, RowVisibility derivation, device-principal checks);
// drift away from this file is detected by archtest
// BASELINE-ROLE-STRING-SINGLE-SOURCE-01 (#1915).

// RoleAdmin is the canonical role name for administrative privilege.
const RoleAdmin = "admin"

// RoleSuperAdmin is the canonical role name for platform super-admin privilege.
// A super-admin principal derives RowScopeAll (cross-tenant visibility), which
// triggers a mandatory slog.Error audit event in Principal.RowVisibility (FR-007).
//
// Production issuer (#1898): the super-admin path is a sanctioned production path
// — there is NO separate super-admin minting mechanism and none is needed.
// sessionmint.MintAccess signs the subject's stored role names into the JWT
// "roles" claim with no role allowlist, so any user whose role record contains
// "superadmin" obtains a super-admin access token; the ordinary user mint
// (mintUserPrincipal) copies the role through, and RowVisibility derives
// RowScopeAll + the mandatory audit. This constant also satisfies
// PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 for the super-admin branch in rowscope.go.
//
// JWT wire form: the expected value in the JWT "roles" claim is "superadmin"
// (one word, no separator). This must match the string literal below exactly.
const RoleSuperAdmin = "superadmin"
