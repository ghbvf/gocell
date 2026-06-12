package authz

// Permission is a sealed, closed-set authorization action identifier.
//
// A Permission names WHAT a request wants to do (e.g. "audit:read"), decoupled
// from WHO may do it (a role). It is the value a route declares via
// auth.RequirePermission(...) and the value carried as the `action` into a
// PDP (auth.Authorizer.Authorize). Keeping it a distinct type — not a bare
// string — is the point: a role-name string literal (e.g. "admin") cannot be
// passed where a Permission is expected, so the #914 migration cannot regress
// into a role-literal gate by accident.
//
// # Sealed construction (closed value set)
//
// The single field `s` is unexported and the only minter is the unexported
// newPermission, called solely from the package-level var declarations below.
// Outside this package there is NO way to construct a Permission:
// authz.Permission{s: "anything"} is a compile error (unexported field), and
// there is no exported constructor or Unmarshal. The set of Permissions that
// can ever exist is therefore exactly the exported Perm* vars in this file — a
// closed, audited registry. The zero value (Permission{}) is invalid; IsZero()
// reports it and downstream consumers fail closed on it.
//
// # AI-robust Grade
//
// Hard — "string-typed concept funnel" + "sealed construction" from the
// ai-robust.md Hard 范本目录. Upstream: newPermission is the sole minter,
// reachable only through this file's registry. Downstream: auth.RequirePermission
// is the sole route consumer. A new permission cannot be introduced without
// adding a Perm* var here (which the registry test pins), so the action
// vocabulary cannot drift via scattered string literals.
//
// Growth: PR-10a seeds only PermAuditRead (the #914 / auditquery target). PR-10b
// adds one Perm* var per migrated endpoint; the closed set grows only here.
type Permission struct {
	s string
}

// newPermission is the sole minter of Permission values. It is unexported and
// must be called only from the package-level Perm* var declarations in this
// file, which is what makes the exported set closed and audited.
func newPermission(s string) Permission {
	return Permission{s: s}
}

// PermAuditRead authorizes reading the audit ledger across actors within the
// caller's tenant (the gate auditquery's "query other actors" branch consults).
// Migrated from the role-literal auth.AnyRole(RoleAdmin, RoleSuperAdmin) gate in
// #914 (PR-10a).
var PermAuditRead = newPermission("audit:read")

// allPermissions is the closed registry of every Permission that exists. It
// backs Permissions() and lets tests pin the closed set (anti-vacuity: a new
// Perm* var that is not added here is caught by the registry test).
var allPermissions = []Permission{
	PermAuditRead,
}

// String returns the action spelling carried into a PDP and stored in policy
// Action targets (e.g. "audit:read"). The zero value returns "" — never a valid
// action — so a forged/zero Permission cannot match a real policy rule.
func (p Permission) String() string {
	return p.s
}

// IsZero reports whether p is the invalid zero value (no minter ran). Consumers
// that receive a Permission from an untyped path should reject IsZero() to fail
// closed.
func (p Permission) IsZero() bool {
	return p.s == ""
}

// Permissions returns a copy of the closed Permission registry. Intended for
// tests and conformance enumeration; the returned slice is independent so a
// caller cannot mutate the registry.
func Permissions() []Permission {
	out := make([]Permission, len(allPermissions))
	copy(out, allPermissions)
	return out
}
