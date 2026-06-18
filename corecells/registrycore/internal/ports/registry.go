// Package ports defines the driven-side interfaces for registrycore (303-US5,
// #2236). The Registry interface is the durable contract_registrations store
// behind which both the in-memory (internal/mem) and PostgreSQL
// (internal/adapters/postgres) implementations sit; US6 (#2245) swaps the cell's
// submit/list services from the bare in-mem kernel ContractRegistrar onto this
// interface (mem in demo/no-PG topology, PG when composed).
package ports

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Registry persists runtime-submitted contract registrations and their
// append-only migration history, scoped per tenant. It is the durable analogue
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

	// List returns up to limit registrations in tenant t whose id is strictly
	// greater than afterID (empty afterID = first page), ordered by id ascending.
	// The caller may request limit+1 to detect a further page (N+1 hasMore).
	List(ctx context.Context, t tenant.TenantID, afterID string, limit int) ([]registry.ContractRegistration, error)

	// History returns the append-only migration event stream for (t, id), ordered
	// by Seq ascending, or (nil, nil) if the id is unknown in tenant t.
	History(ctx context.Context, t tenant.TenantID, id string) ([]registry.RegistrationEvent, error)
}
