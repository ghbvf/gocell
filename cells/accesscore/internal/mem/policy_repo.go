package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

var _ ports.PolicyRepository = (*PolicyRepository)(nil)

// PolicyRepository is the in-memory implementation of ports.PolicyRepository.
//
// Unlike RoleRepository, PolicyRepository owns its own mutex and its own
// tenant-partitioned map — Policies have no cross-repo invariant with roles or
// users (no at-most-one-admin style invariant), so folding this into the shared
// Store mutex would add contention without correctness benefit.
//
// All methods are tenant-scoped (#1337 PR-6): the tenant.TenantID positional
// parameter selects the per-tenant partition of the underlying map.
//
// Clone discipline: every read path (GetByID, ListByTenant) returns a deep copy
// of the stored Policy so callers cannot mutate stored state through the
// returned pointer.
type PolicyRepository struct {
	mu sync.RWMutex
	// policies maps tenantID → (policyID → *abac.Policy).
	// The inner pointer is always the authoritative stored copy; callers receive
	// a clone, never the pointer itself.
	policies map[tenant.TenantID]map[string]*abac.Policy
}

// NewPolicyRepository constructs an empty in-memory PolicyRepository.
func NewPolicyRepository() *PolicyRepository {
	return &PolicyRepository{
		policies: make(map[tenant.TenantID]map[string]*abac.Policy),
	}
}

// Save persists or replaces the policy within the tenant. Validates the tenant
// identity, checks that p.TenantID == t (programmer-error guard), then runs
// Policy.Validate before writing. Returns KindInvalid for any structural
// violation.
func (r *PolicyRepository) Save(ctx context.Context, t tenant.TenantID, p *abac.Policy) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: invalid tenant", err)
	}
	if p.TenantID != t {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithDetails(
				errcode.PublicString("policyTenantId", string(p.TenantID)),
				errcode.PublicString("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.tenantPolicies(t)
	m[p.ID] = clonePolicy(p)
	return nil
}

// GetByID returns the policy identified by id within the tenant. Returns a
// defensive clone so callers cannot mutate stored state. Returns KindNotFound
// when the policy does not exist.
func (r *PolicyRepository) GetByID(ctx context.Context, t tenant.TenantID, id string) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: invalid tenant", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.policies[t]
	if !ok {
		return nil, r.notFound(id)
	}
	p, ok := m[id]
	if !ok {
		return nil, r.notFound(id)
	}
	return clonePolicy(p), nil
}

// ListByTenant returns all policies owned by the tenant. Returns an empty
// (non-nil) slice when the tenant has no policies. Each returned policy is a
// defensive clone.
func (r *PolicyRepository) ListByTenant(ctx context.Context, t tenant.TenantID) ([]*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: invalid tenant", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.policies[t]
	if !ok {
		return []*abac.Policy{}, nil
	}
	result := make([]*abac.Policy, 0, len(m))
	for _, p := range m {
		result = append(result, clonePolicy(p))
	}
	return result, nil
}

// Delete removes the policy identified by id from the tenant. Returns
// KindNotFound when the policy does not exist in t.
func (r *PolicyRepository) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: invalid tenant", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.policies[t]
	if !ok {
		return r.notFound(id)
	}
	if _, exists := m[id]; !exists {
		return r.notFound(id)
	}
	delete(m, id)
	return nil
}

// tenantPolicies returns (or lazily initializes) the per-tenant inner map.
// Caller MUST hold r.mu (write).
func (r *PolicyRepository) tenantPolicies(t tenant.TenantID) map[string]*abac.Policy {
	m, ok := r.policies[t]
	if !ok {
		m = make(map[string]*abac.Policy)
		r.policies[t] = m
	}
	return m
}

// notFound returns a KindNotFound error for the given policy id.
func (r *PolicyRepository) notFound(id string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrAuthPolicyNotFound, "policy not found",
		errcode.WithCategory(errcode.CategoryDomain),
		errcode.WithInternal(errcode.InternalAttr("policy_id", id)))
}

// clonePolicy returns a deep copy of p so that mutations to the returned
// pointer do not affect the stored original (and vice versa).
func clonePolicy(p *abac.Policy) *abac.Policy {
	clone := *p
	clone.Rules = make([]abac.Rule, len(p.Rules))
	for i, r := range p.Rules {
		rClone := r
		if r.Conditions != nil {
			rClone.Conditions = make([]abac.Condition, len(r.Conditions))
			for j, c := range r.Conditions {
				cClone := c
				if c.Values != nil {
					cClone.Values = make([]string, len(c.Values))
					copy(cClone.Values, c.Values)
				}
				rClone.Conditions[j] = cClone
			}
		}
		if r.Obligations.FieldMask.Fields != nil {
			fields := make([]string, len(r.Obligations.FieldMask.Fields))
			copy(fields, r.Obligations.FieldMask.Fields)
			rClone.Obligations.FieldMask.Fields = fields
		}
		clone.Rules[i] = rClone
	}
	return &clone
}
