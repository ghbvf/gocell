package ports

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// RowScopeAllUnsupportedError reports that a tenant.RowVisibility carrying
// tenant.RowScopeAll reached an accesscore row-scoped read path
// (UserRepository.GetByIDInTenant / RoleRepository.GetByUserID / ListByUserID).
//
// accesscore has NO cross-tenant admin read pool (unlike audit's
// gocell_audit_admin, #1810): its tables are served only by the app pool under
// FORCE RLS. A super-admin's principal derives RowScopeAll, but there is no
// sanctioned cross-tenant accesscore read endpoint, so these reads fail-closed
// unconditionally — defense in depth mirroring the audit SERVING pool
// (ledger.RowScopeAllUnsupportedError). Opening a cross-tenant accesscore read
// path is out of scope for #1709 and would need its own role-scoped pool + ADR.
//
// Callers (handlers) MUST surface this error as HTTP 501 Not Implemented
// (KindNotImplemented → 501), distinct from HTTP 404 (row absent). The
// diagnostic distinction matters: 501 signals "cross-tenant read is architecturally
// unsupported on this endpoint", whereas 404 signals "the specific row does not
// exist" — confusing them would mask super-admin misconfiguration (#1709).
//
// It is shared by the PG and mem implementations so the rejection is
// byte-identical across backends and asserted uniformly by the conformance
// suite. The FR-007 cross-tenant audit slog.Error is emitted upstream (inside
// auth.Principal.CrossTenantVisibility) before the store is consulted, so the
// audit fires regardless of this error.
func RowScopeAllUnsupportedError() error {
	return errcode.New(errcode.KindNotImplemented, errcode.ErrInternal,
		"accesscore: RowScopeAll is not supported on this read path")
}
