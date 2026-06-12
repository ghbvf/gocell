package ports

import (
	"context"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// ResourceAttributeProvider is the ABAC PIP (Policy Information Point) source
// for RESOURCE-category attributes.
//
// This interface is intentionally separate from PolicyRepository: resource
// attributes describe protected objects (their classification, owner, sensitivity
// level), not policy aggregates. Conflating both on a single repository would
// violate the single-responsibility principle and leak resource-domain concerns
// into the policy-store aggregate boundary.
//
// Tenancy: every method is tenant-scoped. The tenant MUST be the same
// ctx-derived tenant as the policy load so that policy evaluation and resource
// attribute fetch share a single tenant binding (RESOURCE-ATTR-TENANT-SHARING-01).
// The evaluator fetches resource attributes inside the same scopedtx.Do block
// that loads policies, bound to a single ctx-derived TenantID — the structural
// guarantee that the two reads are tenant-consistent.
//
// PG-backed implementation is a follow-up issue (#1347 follow-up). The mem
// implementation (corecells/accesscore/internal/mem.ResourceAttributeProvider)
// is the real mem-mode impl used in tests and demo wiring.
type ResourceAttributeProvider interface {
	// GetAttributes returns the resource's attributes for ABAC condition
	// evaluation, scoped to tenant t. Returns an empty (non-nil) map when the
	// resource has no attributes. The tenant MUST be the same ctx-derived tenant
	// as the policy load (RESOURCE-ATTR-TENANT-SHARING-01).
	//
	// The return shape map[string][]string mirrors the evaluator's multi-valued
	// attribute model: a key may resolve to multiple values (e.g. "tags":
	// ["sensitive", "pii"]) that membership operators (in / not_in) evaluate over.
	GetAttributes(ctx context.Context, resourceID string, t tenant.TenantID) (map[string][]string, error)
}
