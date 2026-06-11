package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

var _ ports.PolicyRepository = (*PolicyRepository)(nil)

const msgPolicyInvalidTenant = "policy_repo: invalid tenant"

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
//
// Versioning: Create sets Version=1. Update checks expectedVersion against the
// stored version (CAS); on match it bumps Version++ and stores the new state.
// Delete checks expectedVersion the same way and removes the entry on match.
// mem has no clock dependency — Policy carries no timestamp fields.
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

// Create inserts a new policy within the tenant. Validates the tenant identity,
// checks that p.TenantID == t (programmer-error guard), then runs Policy.Validate
// before writing. Returns ErrAuthPolicyDuplicate (KindConflict) when a policy
// with the same id already exists in t. Sets Version=1 on the stored copy.
func (r *PolicyRepository) Create(_ context.Context, t tenant.TenantID, p *abac.Policy) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	if p == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy must not be nil")
	}
	if p.TenantID != t {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithInternal(
				errcode.InternalAttr("policyTenantId", string(p.TenantID)),
				errcode.InternalAttr("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.tenantPolicies(t)
	if _, exists := m[p.ID]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthPolicyDuplicate, "policy already exists",
			errcode.WithInternal(errcode.InternalAttr("policy_id", p.ID)))
	}
	clone := clonePolicy(p)
	clone.Version = 1
	m[p.ID] = clone
	return nil
}

// Update atomically replaces the policy if expectedVersion matches the stored
// version (CAS guard). Returns ErrAuthPolicyNotFound when the policy does not
// exist, or ErrVersionConflict when expectedVersion mismatches. On success,
// bumps Version++ and returns the stored clone.
func (r *PolicyRepository) Update(
	_ context.Context, t tenant.TenantID, id string, expectedVersion int, p *abac.Policy,
) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy must not be nil")
	}
	if p.TenantID != t {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithInternal(
				errcode.InternalAttr("policyTenantId", string(p.TenantID)),
				errcode.InternalAttr("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.policies[t]
	if !ok {
		return nil, r.notFound(id)
	}
	existing, ok := m[id]
	if !ok {
		return nil, r.notFound(id)
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "policy", id)
	}
	clone := clonePolicy(p)
	clone.Version = existing.Version + 1
	m[id] = clone
	return clonePolicy(clone), nil
}

// Delete removes the policy if expectedVersion matches the stored version (CAS
// guard). Returns ErrAuthPolicyNotFound when absent, or ErrVersionConflict on
// mismatch. On success, returns the deleted policy.
func (r *PolicyRepository) Delete(_ context.Context, t tenant.TenantID, id string, expectedVersion int) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.policies[t]
	if !ok {
		return nil, r.notFound(id)
	}
	existing, ok := m[id]
	if !ok {
		return nil, r.notFound(id)
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "policy", id)
	}
	clone := clonePolicy(existing)
	delete(m, id)
	return clone, nil
}

// GetByID returns the policy identified by id within the tenant. Returns a
// defensive clone so callers cannot mutate stored state. Returns KindNotFound
// when the policy does not exist.
func (r *PolicyRepository) GetByID(_ context.Context, t tenant.TenantID, id string) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
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
func (r *PolicyRepository) ListByTenant(_ context.Context, t tenant.TenantID) ([]*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
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

// RepoReady reports store readiness. The in-memory PolicyRepository has no
// external dependency, so it is always ready (returns nil). It exists to satisfy
// ports.PolicyRepository (and thus kernel/healthz.RepoProber) uniformly across
// implementations: the cell folds every repo's readiness into one probe, and a
// mem store that omitted this would be invisible to that aggregate (#1346 PR-8).
func (r *PolicyRepository) RepoReady(_ context.Context) error { return nil }

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
