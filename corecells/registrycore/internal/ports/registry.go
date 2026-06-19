// Package ports defines the driven-side interfaces for registrycore (303-US5,
// #2236). The Registry interface is the durable contract_registrations store
// behind which both the in-memory (internal/mem) and PostgreSQL
// (internal/adapters/postgres) implementations sit; US6 (#2237) swaps the cell's
// submit/list services from the bare in-mem kernel ContractRegistrar onto this
// interface (mem in demo/no-PG topology, PG when composed).
package ports

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// ListFilter carries optional predicates for Registry.List. The zero value
// (ListFilter{}) means "no filter — return all states". A non-zero State
// restricts results to registrations in that exact state.
type ListFilter struct {
	// State is the optional state predicate. The zero value (RegistrationState{})
	// means no state filter (all states returned). Use registry.ParseState to
	// construct a non-zero value from a wire string.
	State registry.RegistrationState
}

// Registry persists runtime-submitted contract registrations and their
// append-only migration history, scoped per tenant. It is the durable analog
// of the in-mem kernel registry.ContractRegistrar: the method set mirrors the
// registrar (Submit→Create, Advance→Transition, Get, AllIDs→List, Events→History)
// so US6's service swap is mechanical.
//
// # Tenancy (303-US5, #2236; mirrors configcore #1337 PR-2b)
//
// Every method takes tenant.TenantID as a mandatory typed positional parameter
// (param[1], immediately after ctx); "漏传 tenant" is a compile error (the
// type-system Hard guarantee) and the position is backstopped by archtest
// TENANT-REPO-PARAM-FUNNEL-01. There is NO tenant-less carve-out: a registration
// id is unique only within a tenant (PK (tenant_id, id)), so even the by-id Get is
// tenant-scoped. Cross-tenant rows return not-found, never another tenant's data.
//
// # Atomicity & legality (L1)
//
// Create and Transition are L1 local-transaction operations: the projection write
// and the append-only history write land in one transaction (write fails → roll
// back, no half state). Transition's legality is the kernel's single source of
// truth — implementations validate via registry.Transition(from, to) and MUST NOT
// re-encode the state machine.
//
// # Read scoping under RLS (caller obligation; #2236 review F1, wired #2392)
//
// The PG tables carry FORCE ROW LEVEL SECURITY (migration 066). Under the restricted
// serving role (NOBYPASSRLS) the tenant_isolation policy fail-closes any access whose
// app.tenant_id GUC is unset — an UNSCOPED read returns 0 rows and an unscoped write
// is rejected (proven by TestContractRegistrations_RLS_TenantIsolation_ServingRole).
// The GUC is injected (SET LOCAL) only inside TxManager.RunInTx. Writes already require
// an ambient tx; the read methods (Get/List/History) impose the same caller obligation:
// run them within a tenant-scoped tx (tenant.WithScope(ctx, t) + RunInTx). The bound
// read service satisfies it through the registrycore/internal/scopedread funnel (#2392),
// the sole production caller of tenant.WithScope in registrycore (pinned by
// TENANT-TXSCOPE-WRITE-CALLER-01), mirroring configcore/internal/scopedread. Today List
// is the only read with a production caller; Get/History carry the same obligation on any
// future caller. The funnel guards the tenant.WithScope writer, NOT each repo-read
// callsite — a read that bypasses scopedread is not a compile error, it fail-closes to 0
// rows under RLS (no leak, but no correct data). The typed tenant param is the primary
// isolation; RLS is the DB-Hard backstop the caller keeps effective by scoping reads.
type Registry interface {
	// Create records a new submission in the submitted state plus its initial
	// migration event (From = zero sentinel, To = submitted), atomically, scoped to
	// tenant t. in is validated (registry.SubmitInput.Validate) and normalized.
	// Returns ErrRegistrationDuplicate if (t, in.ID) already exists,
	// ErrValidationFailed for a missing required field.
	Create(ctx context.Context, t tenant.TenantID, in registry.SubmitInput) (registry.ContractRegistration, error)

	// Transition advances (t, in.ID) to in.To in one L1 transaction: read the
	// current state (locked), validate the transition via registry.Transition,
	// update the projection, and append one migration event. When in.To is
	// approved, in.Actor is recorded as the registration's Approver. Returns
	// ErrRegistrationNotFound for an unknown id, ErrRegistrationInvalidTransition
	// for an illegal/terminal/self transition, ErrValidationFailed for an empty
	// in.ID or in.Actor.
	Transition(ctx context.Context, t tenant.TenantID, in registry.AdvanceInput) (registry.ContractRegistration, error)

	// Get returns the registration (t, id), or (zero, false, nil) if no such row
	// exists in tenant t.
	Get(ctx context.Context, t tenant.TenantID, id string) (registry.ContractRegistration, bool, error)

	// List returns up to params.FetchLimit() registrations in tenant t using
	// keyset pagination ordered by id ASC (the only supported sort column).
	// params.CursorValues should be nil for the first page and contain the last
	// seen id (as a string) on subsequent pages. The caller requests
	// params.FetchLimit() = params.Limit+1 rows to detect a further page via the
	// N+1 hasMore pattern; the caller trims the result to Limit before building
	// the cursor. filter.State is an optional state predicate: the zero value
	// (ListFilter{}) means "all states".
	List(ctx context.Context, t tenant.TenantID, params query.ListParams, filter ListFilter) ([]registry.ContractRegistration, error)

	// History returns the append-only migration event stream for (t, id), ordered
	// by Seq ascending. An empty result (nil or empty slice) means "no events" —
	// callers MUST NOT distinguish an unknown id from one with no events (a
	// registration always has at least its submit event, so an empty stream in
	// practice means the id does not exist in tenant t); use Get for existence.
	History(ctx context.Context, t tenant.TenantID, id string) ([]registry.RegistrationEvent, error)
}
