package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

var _ ports.ResourceAttributeProvider = (*ResourceAttributeProvider)(nil)

const msgResourceAttrInvalidTenant = "resource_attr: invalid tenant"

// ResourceAttributeProvider is the in-memory implementation of
// ports.ResourceAttributeProvider (ABAC PIP for resource attributes).
//
// Empty by default: no attributes are seeded at construction time. A
// SourceResource condition against an unseeded resource evaluates found=false
// in the attribute resolver, making the condition unsatisfied and the enclosing
// rule non-matching — resource conditions stay fail-closed until attributes
// are seeded (behavior-preserving: same semantics as PR-7 which returned
// nil,false unconditionally). This is NOT an unsafe noop: denying by absence
// is the correct fail-closed default for an ABAC PDP.
//
// For production deployments the PG-backed resource_attributes store is the
// follow-up issue (#1347 follow-up); mem is the intended impl for tests, demo
// mode, and local development where resource-attribute conditions are not
// exercised.
//
// Clone discipline: GetAttributes returns a deep copy of the stored attribute
// map so callers cannot mutate stored state through the returned map or slice
// values (mirrors the clone discipline in mem.PolicyRepository).
//
// Thread safety: own sync.RWMutex guards the tenant-keyed map; no shared
// Store dependency (resource attributes share no cross-repo invariant with
// users, roles, or policies).
type ResourceAttributeProvider struct {
	mu sync.RWMutex
	// attrs maps tenantID → (resourceID → (attrKey → []string)).
	// The inner slices are the authoritative stored copies; callers receive
	// deep clones, never the original slices.
	attrs map[tenant.TenantID]map[string]map[string][]string
}

// NewResourceAttributeProvider constructs an empty in-memory
// ResourceAttributeProvider. Resource conditions evaluate fail-closed (deny)
// until attributes are seeded via Seed. See type godoc for the business reason.
func NewResourceAttributeProvider() *ResourceAttributeProvider {
	return &ResourceAttributeProvider{
		attrs: make(map[tenant.TenantID]map[string]map[string][]string),
	}
}

// GetAttributes returns the attribute map for resourceID within tenant t.
// Returns an empty (non-nil) map when the tenant or resource is unknown.
// The returned map is a deep clone; mutations do not affect stored state.
// Validates t before accessing storage (fail-fast on invalid tenant).
func (p *ResourceAttributeProvider) GetAttributes(
	_ context.Context, resourceID string, t tenant.TenantID,
) (map[string][]string, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgResourceAttrInvalidTenant, err)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantAttrs, ok := p.attrs[t]
	if !ok {
		return make(map[string][]string), nil
	}
	resAttrs, ok := tenantAttrs[resourceID]
	if !ok {
		return make(map[string][]string), nil
	}
	return cloneAttrMap(resAttrs), nil
}

// Seed stores (or replaces) the attribute map for resourceID within tenant t.
// Intended for tests and seeding demo / fixture data. Validates t at entry.
// attrs may be nil (equivalent to an empty map — removes all attributes for
// the resource). Seed is safe for concurrent use.
func (p *ResourceAttributeProvider) Seed(t tenant.TenantID, resourceID string, attrs map[string][]string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgResourceAttrInvalidTenant, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.attrs[t]; !ok {
		p.attrs[t] = make(map[string]map[string][]string)
	}
	if attrs == nil {
		delete(p.attrs[t], resourceID)
		return nil
	}
	p.attrs[t][resourceID] = cloneAttrMap(attrs)
	return nil
}

// cloneAttrMap returns a deep copy of the attribute map (both the map itself
// and each slice value) so neither the caller nor the store can mutate the
// other's copy.
func cloneAttrMap(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, vals := range m {
		cloned := make([]string, len(vals))
		copy(cloned, vals)
		out[k] = cloned
	}
	return out
}
