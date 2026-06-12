package ports

import (
	"context"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// MsgInvalidTenant is the shared error message used by PolicyRepository
// implementations when the tenant.TenantID parameter fails validation. Exported
// so both mem and postgres implementations reference the same literal (#6).
const MsgInvalidTenant = "policy_repo: invalid tenant"

// PolicyRepository persists and retrieves ABAC Policy aggregates owned by a
// tenant.
//
// Tenancy (#1337 PR-6): policies are per-tenant — every method takes a mandatory
// tenant.TenantID positional parameter immediately after ctx ("in tenant T,
// do X"). A policy ID is unique only within a tenant, so even a by-id read must
// be tenant-scoped. Implementations apply per-tenant partitioning (PG: AND
// tenant_id = $N; mem: per-tenant map) and reject an invalid tenant via
// tenant.TenantID.Validate at method entry (fail-fast).
//
// Consistency level: L1 — policies are authorisation metadata stored with
// optimistic-concurrency (CAS) versioning. Version=1 is set on Create and
// incremented on every Update. Callers supply the last-read version as
// expectedVersion to Update/Delete; a mismatch returns ErrVersionConflict
// (KindConflict). Higher-level operations (evaluation, audit) build on top.
//
// Write surface:
//   - Create: insert; KindConflict (ErrAuthPolicyDuplicate) if (tenant, id) exists.
//   - Update: CAS replace; KindNotFound if absent, KindConflict
//     (ErrVersionConflict) if expectedVersion mismatches.
//   - Delete: CAS delete; KindNotFound if absent, KindConflict
//     (ErrVersionConflict) if expectedVersion mismatches.
//
// Implementations must return defensive copies on every read path (GetByID,
// ListByTenant) so callers cannot inadvertently mutate stored state.
type PolicyRepository interface {
	// Create inserts a new policy for the tenant. The policy's own TenantID
	// field must equal t; a mismatch is a programmer error returned as
	// KindInvalid. The policy is validated before being stored (Policy.Validate).
	// The repository sets Version=1 on the stored copy.
	// Returns the persisted clone with Version=1 set (symmetry with Update/Delete).
	// Returns ErrAuthPolicyDuplicate (KindConflict) when (tenant, p.ID) already
	// exists.
	Create(ctx context.Context, t tenant.TenantID, p *abac.Policy) (*abac.Policy, error)
	// Update atomically replaces the policy and bumps Version if expectedVersion
	// matches the stored version (CAS guard). The policy's TenantID must equal t.
	// The policy is validated before being stored (Policy.Validate).
	// Returns ErrAuthPolicyNotFound (KindNotFound) if the policy does not exist
	// in t, or ErrVersionConflict (KindConflict) if expectedVersion does not
	// match the stored version. On success, returns the stored clone with the
	// incremented version.
	Update(ctx context.Context, t tenant.TenantID, id string, expectedVersion int, p *abac.Policy) (*abac.Policy, error)
	// Delete removes the policy identified by id if expectedVersion matches the
	// stored version (CAS guard). Returns ErrAuthPolicyNotFound (KindNotFound)
	// if the policy does not exist in t, or ErrVersionConflict (KindConflict) if
	// expectedVersion does not match. On success, returns the deleted policy so
	// the caller can emit its version in events.
	Delete(ctx context.Context, t tenant.TenantID, id string, expectedVersion int) (*abac.Policy, error)
	// GetByID returns the policy identified by id within the tenant.
	// Returns KindNotFound when the policy does not exist in t.
	GetByID(ctx context.Context, t tenant.TenantID, id string) (*abac.Policy, error)
	// ListByTenant returns ALL policies owned by the tenant in a single call.
	// Returns an empty slice (not nil) when the tenant has no policies.
	//
	// No pagination is provided by design: per-tenant policy count is bounded by
	// management-plane cardinality (policies are authored by administrators, not
	// generated at data-plane scale). The evaluation engine (PR-7) performs a
	// full-load of all policies for each authorization decision; a paginated
	// interface would require multiple round-trips or a cursor-iteration wrapper
	// in hot-path code, adding complexity with no practical benefit given the
	// expected cardinality. This is not an HTTP list endpoint — it is an internal
	// eval-time read path that must return a complete, consistent snapshot.
	ListByTenant(ctx context.Context, t tenant.TenantID) ([]*abac.Policy, error)
	// RepoReady reports whether the backing store is reachable. It satisfies
	// kernel/healthz.RepoProber so the cell can fold policy-store readiness into
	// the cell-level readiness probe (#1346 PR-8, T8.4). mem returns nil
	// unconditionally (in-memory is always ready); PG runs a lightweight probe
	// against the policies table (table reachable ⇒ ready, including the empty
	// table). The signature matches healthz.RepoProber.RepoReady exactly.
	RepoReady(ctx context.Context) error
}
