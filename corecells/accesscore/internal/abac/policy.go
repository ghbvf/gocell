package abac

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// Policy is the top-level ABAC authorization artifact owned by a tenant.
// A Policy groups one or more Rules that are evaluated together. The GoCell
// ABAC engine evaluates all Rules in a Policy and applies deny-overrides
// semantics (any EffectDeny wins over any number of EffectAllow results),
// mirroring XACML §7.16 and AWS Cedar's forbid-overrides-permit behavior.
//
// Version is a repo-owned optimistic-concurrency counter: the repository sets
// Version=1 on Create and increments it on every Update. Callers supply the
// last-read Version as expectedVersion to Update/Delete (CAS guard). The
// policy.updated event carries Version so consumers can detect gaps.
// Validate() does not check Version (it is repo-owned and zero for new
// aggregates before persistence). Timestamp fields (created_at / updated_at)
// remain infrastructure-only DB columns, not domain fields.
//
// ref: XACML 3.0 §5.8 — Policy element.
// ref: AWS Cedar policy model — policy set / policy / rule decomposition.
type Policy struct {
	// ID is the globally-unique (within the tenant) policy identifier.
	// Must be non-empty. Treated as opaque by the engine; callers are
	// responsible for uniqueness within the tenant.
	ID string
	// TenantID is the isolation domain that owns this policy. Must be a valid
	// canonical lowercase UUID (tenant.TenantID.Validate()). Enforced fail-fast
	// in Create/Update/Delete — a mismatched TenantID is a programmer error, not
	// a user error.
	TenantID tenant.TenantID
	// Name is a human-readable label. Must be non-empty.
	Name string
	// Description is an optional human-readable description. May be empty.
	Description string
	// Rules is the ordered list of authorization rules. Must contain at least
	// one rule. Rule IDs must be unique within the policy.
	Rules []Rule
	// Version is the optimistic-concurrency counter owned by the repository.
	// Set to 1 on Create; incremented by 1 on every successful Update.
	// Callers pass the last-read Version as expectedVersion to Update/Delete.
	// Zero before first persistence; Validate() does not enforce a minimum.
	Version int
}

// Validate returns an error if the Policy is structurally invalid:
//   - ID must be non-empty.
//   - TenantID must pass tenant.TenantID.Validate() (non-empty canonical UUID).
//   - Name must be non-empty.
//   - Rules must contain at least one Rule.
//   - Each Rule must pass Rule.Validate().
//   - Rule IDs must be unique within the Policy.
func (p *Policy) Validate() error {
	if p.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: policy ID must not be empty")
	}
	if err := p.TenantID.Validate(); err != nil {
		return err
	}
	if p.Name == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: policy Name must not be empty")
	}
	if len(p.Rules) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: policy must contain at least one rule")
	}
	seen := make(map[string]struct{}, len(p.Rules))
	for _, rule := range p.Rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if _, dup := seen[rule.ID]; dup {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: policy contains duplicate rule ID",
				errcode.WithDetails(errcode.PublicString("ruleId", rule.ID)))
		}
		seen[rule.ID] = struct{}{}
	}
	return nil
}
