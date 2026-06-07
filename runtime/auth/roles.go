package auth

// RoleAdmin is the canonical role name for administrative privilege.
const RoleAdmin = "admin"

// RoleSuperAdmin is the canonical role name for platform super-admin privilege.
// A super-admin principal derives RowScopeAll (cross-tenant visibility), which
// triggers a mandatory slog.Error audit event in Principal.RowVisibility (FR-007).
// No production token issuer assigns this role on develop yet — it is a
// type-foundation constant (same pattern as PrincipalDevice) that allows
// test-injection and satisfies PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 for the
// super-admin branch in rowscope.go.
const RoleSuperAdmin = "superadmin"
