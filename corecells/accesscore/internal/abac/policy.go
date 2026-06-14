package abac

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
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

// Clone returns a deep copy of p so that mutations to the returned pointer do not
// affect the original and vice versa. The copy covers:
//   - top-level scalar fields (ID, TenantID, Name, Description, Version)
//   - Rules slice (new backing array)
//   - each Rule's Action slice (new backing array)
//   - each Rule's Conditions slice (new backing array per rule)
//   - each Condition's Values slice (new backing array)
//   - each Rule's Obligations.FieldMask.Fields slice (new backing array)
//
// This is the single-source deep-clone used by all PolicyRepository
// implementations (B2 — eliminates the duplicated clonePolicy helpers in mem and
// postgres adapters).
func (p *Policy) Clone() *Policy {
	c := *p
	c.Rules = make([]Rule, len(p.Rules))
	for i, r := range p.Rules {
		rc := r
		// Action is an action-target slice (PR-10a #1348); deep-copy it like
		// Conditions/FieldMask so a caller mutating a returned rule's Action
		// cannot alias back into the stored policy (repo defensive-clone isolation).
		if r.Action != nil {
			action := make([]string, len(r.Action))
			copy(action, r.Action)
			rc.Action = action
		}
		if r.Conditions != nil {
			rc.Conditions = make([]Condition, len(r.Conditions))
			for j, cond := range r.Conditions {
				cc := cond
				if cond.Values != nil {
					cc.Values = make([]string, len(cond.Values))
					copy(cc.Values, cond.Values)
				}
				rc.Conditions[j] = cc
			}
		}
		if r.Obligations.FieldMask.Fields != nil {
			fields := make([]string, len(r.Obligations.FieldMask.Fields))
			copy(fields, r.Obligations.FieldMask.Fields)
			rc.Obligations.FieldMask.Fields = fields
		}
		c.Rules[i] = rc
	}
	return &c
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
