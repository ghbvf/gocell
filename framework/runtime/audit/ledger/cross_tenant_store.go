package ledger

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// CrossTenantQueryStore is the sanctioned super-admin cross-tenant audit read
// path (#1810). It is deliberately SEPARATE from Store / QueryStore: the ordinary
// serving-pool backends (MemStore, PG LedgerStore, MultiStore) keep fail-closing
// RowScopeAll (RowScopeAllUnsupportedError) because the NOBYPASSRLS serving role
// cannot read across tenants. A CrossTenantQueryStore is instead backed by a
// dedicated role-scoped admin read pool (PG role gocell_audit_admin + a
// role-scoped permissive RLS SELECT policy USING(true)), so it can read every
// tenant's rows while FORCE RLS stays ON for the serving role — preserving ADR
// #1676's no-BYPASSRLS invariant (cross-tenant visibility comes from an explicit,
// auditable pg_policy row, not from bypassing RLS).
//
// # Typed funnel + data-layer PEP (#1760, F2)
//
// QueryCrossTenant takes a tenant.CrossTenantVisibility positional parameter, not
// a bare tenant.RowVisibility. CrossTenantVisibility is sealed (its sole producer
// is the audited (*auth.Principal).CrossTenantVisibility derivation), so "forge
// the grant" (a non-zero struct literal) and "forget it" (omit the param) are both
// compile-time impossible — the mandatory FR-007 audit cannot be bypassed.
//
// The one residual Go cannot close is the constructable zero value
// (tenant.CrossTenantVisibility{}, an invalid obligation). Every implementation
// therefore re-validates ctv fail-closed (ctv.Validate) before reading — the
// data-layer PEP: a zero/invalid obligation yields an error, never a cross-tenant
// read. The RunCrossTenantQueryConformance suite pins this for every backend, so
// the fail-close is a machine-checked contract, not a per-impl convention.
//
// The cross-tenant store is an OPTIONAL dependency of the auditquery Service: when
// it is absent (the admin read pool is not provisioned), a super-admin's
// RowScopeAll request stays fail-closed at HTTP 501 (RowScopeAllUnsupportedError),
// exactly as before #1810 — graceful, fail-closed, never fail-open.
type CrossTenantQueryStore interface {
	// QueryCrossTenant lists audit entries across ALL tenants (and both the relay
	// and bootstrap namespace chains) matching AuditFilters, using the same keyset
	// cursor pagination as Store.Query (params.Limit + decoded CursorValues +
	// params.Sort; params.Sort must be non-empty — callers pass QuerySort). It
	// returns up to params.FetchLimit() rows for N+1 hasMore detection, ordered by
	// params.Sort, and an empty (non-nil) slice when no entries match.
	//
	// Unlike Store.Query there is NO tenant.TenantID parameter: the read spans every
	// tenant by construction. The ctv carries the sealed RowScopeAll obligation; its
	// owner dimension is unrestricted (every actor_id is visible), so AuditFilters
	// (e.g. ActorID) is the only narrowing applied on top of the cross-tenant scope.
	QueryCrossTenant(ctx context.Context, ctv tenant.CrossTenantVisibility, filters AuditFilters, params query.ListParams) ([]*Entry, error)

	// GetByIDCrossTenant fetches a single audit entry by its opaque store id across
	// ALL tenants (and BOTH the relay and bootstrap namespace chains), the
	// single-entry counterpart of QueryCrossTenant and the sanctioned super-admin
	// cross-tenant read for http.audit.get.v1. Returns ErrAuditLedgerNotFound when
	// no entry with that id exists.
	//
	// Unlike Store.GetByID there is NO tenant.TenantID parameter (the read spans
	// every tenant by construction) and NO owner predicate (the RowScopeAll
	// obligation makes every actor_id visible). The id is globally unique on every
	// backend (PG admin pool: uuid primary key; mem: a deterministic
	// namespace+tenant+eventID hash), so the cross-tenant lookup is unambiguous.
	//
	// ctv carries the sealed RowScopeAll obligation; like QueryCrossTenant this
	// method re-validates it fail-closed (ctv.Validate) before reading — the
	// data-layer PEP (F2): the typed param makes "forge/forget the grant" a compile
	// error, and the runtime check rejects Go's constructable zero value, so a
	// zero/invalid obligation can never produce a cross-tenant read.
	GetByIDCrossTenant(ctx context.Context, ctv tenant.CrossTenantVisibility, id string) (*Entry, error)
}

// ErrMsgCrossTenantObligation is the single-source const-literal fail-close message
// (MESSAGE-CONST-LITERAL-01) the CrossTenantQueryStore BACKENDS use when the
// data-layer PEP rejects a zero/invalid CrossTenantVisibility (F2). Exported so the
// mem backend (this package) and the PG AuditCrossTenantStore (adapters/postgres)
// reference one literal — a single edit site, no cross-backend drift. The auditquery
// Service has its OWN service-layer PEP message (errMsgInvalidCrossTenantObligation)
// — defense in depth, deliberately distinct so a log line names which layer rejected;
// it does NOT reuse this constant.
const ErrMsgCrossTenantObligation = "audit ledger: cross-tenant read requires a valid RowScopeAll obligation"
