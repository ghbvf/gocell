package authorizationdecide

// INVARIANT: BASELINE-OWNER-RULE-TENANT-FREEZE-01
//
// BASELINE-OWNER-RULE-TENANT-FREEZE-01 freezes the condition shape of the three
// owner-scoped ALLOW baseline rules (baseline-user-read-self, baseline-user-write-self,
// baseline-role-read-self). Each MUST be an EffectAllow rule carrying EXACTLY ONE condition,
// and that condition MUST equal the ownership shape subject.sub == resource.id (Source=SourceSubject,
// Key="sub", Operator=OpEqualsAttr, RHSSource=SourceResource, RHSKey="id", no static
// Values).
//
// # Threat closed
//
// #1977 moved accesscore self/ownership into the PDP via these baseline rules. Their
// correctness rests on the ownership condition comparing request-local UUIDs — it is
// tenant-AGNOSTIC, so a cross-tenant subject (a different tenant_id claim) is denied
// exactly like a same-tenant non-owner (proven end-to-end by TestABACPDPGatesAccesscore's
// cross_tenant_* cases, #2026). This freeze locks the policy REPRESENTATION so that
// invariant can never silently erode: a future edit adding a tenant-matching grant — e.g.
// subject.tenant_id == resource.tenant_id (cross-attr) or subject.tenant_id in {…}
// (static) — to one of these rules would WIDEN owner-scoped access to tenant-scoped,
// bypassing the principal-derived RowScope tenant isolation that the route gate is a
// defense-in-depth layer over (not a replacement for). Any such addition changes a
// rule's condition count or condition fields and trips this test in the build-test lane.
//
// # AI-robust grade: Medium (value-level golden, same package)
//
// The test calls builtinBaselineRules() (unexported — hence a same-package internal test,
// not a tools/archtest scan) and asserts each owner-scoped ALLOW rule's Conditions slice
// EXACTLY matches frozenOwnerCondition. frozenOwnerCondition is spelled as an INDEPENDENT
// literal (NOT derived from subjectIsResource()) precisely so a change to
// subjectIsResource() — which every owner rule calls — is caught as drift rather than
// silently tracked. A value-golden over the real constructed rules is stronger than an AST
// scan here: it compares the exact struct the evaluator consumes, with no literal-parsing
// fragility. Hard-ification path: when baseline rules are derived from a metadata/contract
// declaration + codegen + byte golden (the PR-13 end state tracked for
// OWNER-SCOPED-GATE-EXACT-SET-01 / PERMISSION-BASED-AUTHZ-01), this test becomes redundant
// and is removed.
//
// # Anti-vacuity
//
// TestBaselineOwnerRuleTenantFreeze_01 asserts all three owner rule IDs were FOUND in
// builtinBaselineRules() — a vacuous green (rules renamed/removed, or the accessor returns
// nothing) trips the "not found" check. TestBaselineOwnerRuleTenantFreeze_01_RejectsTenantWidening
// is the reverse fixture: it feeds crafted tenant-widening (and other drift) shapes through
// the SAME checkOwnerRuleFrozen helper and asserts each is REJECTED, proving the freeze can
// actually fail.
//
// # Blind spots
//
//   - Freezes the rule's Effect (must be EffectAllow) + the CONDITION shape (the tenant-grant
//     vector). It does NOT freeze a rule's Action set — a rule broadening its Action while
//     keeping the ownership condition is a different widening vector (owner→action, not
//     owner→tenant), out of this invariant's scope. OWNER-SCOPED-GATE-EXACT-SET-01 freezes the
//     route-gate (handler|param|perm) set; this freezes the baseline-rule grant shape.
//   - The owner rule ID set (ownerScopedBaselineRuleIDs) is hand-maintained: a NEW
//     owner-scoped ownership rule must be added there, or it is not frozen. The "not found"
//     anti-vacuity check guards renames/removals of the three known IDs, not net-new rules.

import (
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
)

// frozenOwnerCondition is the single authoritative ownership condition shape —
// subject.sub == resource.id — that every owner-scoped baseline ALLOW rule must carry,
// and ONLY that. Spelled independently of subjectIsResource() so a drift in that helper
// is caught, not tracked. See the file godoc.
var frozenOwnerCondition = abac.Condition{
	Source:    abac.SourceSubject,
	Key:       "sub",
	Operator:  abac.OpEqualsAttr,
	RHSSource: abac.SourceResource,
	RHSKey:    "id",
}

// ownerScopedBaselineRuleIDs is the frozen set of baseline rule IDs whose single
// condition must equal frozenOwnerCondition. These are the #1977 identity-ownership
// rules; the admin rules (baseline-*-admin) are role-conditioned and out of scope.
var ownerScopedBaselineRuleIDs = map[string]struct{}{
	"baseline-user-read-self":  {},
	"baseline-user-write-self": {},
	"baseline-role-read-self":  {},
}

// checkOwnerRuleFrozen returns nil iff r is an EffectAllow rule carrying EXACTLY the frozen
// ownership condition (one condition, equal to frozenOwnerCondition field-by-field). A
// non-nil error names the deviation. abac.Condition is compared field-by-field because its
// Values []string member makes it non-comparable with ==; len(Values) != 0 is the check that
// rejects a static tenant match (Values: {tenant…}).
func checkOwnerRuleFrozen(r abac.Rule) error {
	if r.Effect != authz.EffectAllow {
		return fmt.Errorf("rule %q: Effect = %v, want EffectAllow — an owner-scoped rule that is not an "+
			"ALLOW grant (e.g. flipped to Deny) is not the grant shape this invariant freezes", r.ID, r.Effect)
	}
	if len(r.Conditions) != 1 {
		return fmt.Errorf("rule %q: got %d conditions, want exactly 1 (the ownership condition) — "+
			"an extra condition (e.g. a tenant match) would widen owner-scoped access", r.ID, len(r.Conditions))
	}
	got := r.Conditions[0]
	if got.Source != frozenOwnerCondition.Source ||
		got.Key != frozenOwnerCondition.Key ||
		got.Operator != frozenOwnerCondition.Operator ||
		got.RHSSource != frozenOwnerCondition.RHSSource ||
		got.RHSKey != frozenOwnerCondition.RHSKey ||
		len(got.Values) != 0 {
		return fmt.Errorf("rule %q: condition = %+v, want %+v (subject.sub == resource.id, no static Values) — "+
			"any deviation (e.g. Key/RHSKey \"tenant_id\", or a static tenant Values set) widens owner scope to tenant scope",
			r.ID, got, frozenOwnerCondition)
	}
	return nil
}

// TestBaselineOwnerRuleTenantFreeze_01 enforces BASELINE-OWNER-RULE-TENANT-FREEZE-01:
// every owner-scoped baseline ALLOW rule carries exactly the frozen ownership condition,
// and all three owner rule IDs are present (anti-vacuity).
func TestBaselineOwnerRuleTenantFreeze_01(t *testing.T) {
	found := make(map[string]struct{}, len(ownerScopedBaselineRuleIDs))
	for _, r := range builtinBaselineRules() {
		if _, isOwner := ownerScopedBaselineRuleIDs[r.ID]; !isOwner {
			continue
		}
		found[r.ID] = struct{}{}
		if err := checkOwnerRuleFrozen(r); err != nil {
			t.Errorf("BASELINE-OWNER-RULE-TENANT-FREEZE-01: %v", err)
		}
	}
	for id := range ownerScopedBaselineRuleIDs {
		if _, ok := found[id]; !ok {
			t.Errorf("BASELINE-OWNER-RULE-TENANT-FREEZE-01: owner-scoped baseline rule %q not found in "+
				"builtinBaselineRules() — it was removed or renamed without updating ownerScopedBaselineRuleIDs "+
				"(anti-vacuity: the freeze must inspect all three real owner rules)", id)
		}
	}
}

// TestBaselineOwnerRuleTenantFreeze_01_RejectsTenantWidening is the reverse fixture:
// crafted tenant-widening (and other drift) shapes must each be REJECTED by
// checkOwnerRuleFrozen, and the exact frozen shape ACCEPTED — proving the freeze is
// non-vacuous (it can actually fail when a widening condition is introduced).
func TestBaselineOwnerRuleTenantFreeze_01_RejectsTenantWidening(t *testing.T) {
	// tenantCrossAttr is the most dangerous widening: subject.tenant_id == resource.tenant_id
	// grants any user access to any same-tenant resource (owner-scoped → tenant-scoped).
	tenantCrossAttr := abac.Condition{
		Source: abac.SourceSubject, Key: "tenant_id", Operator: abac.OpEqualsAttr,
		RHSSource: abac.SourceResource, RHSKey: "tenant_id",
	}
	tests := []struct {
		name       string
		effect     authz.Effect
		conditions []abac.Condition
		wantErr    bool
	}{
		{
			name:       "frozen_ownership_shape_accepted",
			effect:     authz.EffectAllow,
			conditions: []abac.Condition{frozenOwnerCondition},
			wantErr:    false,
		},
		{
			name:       "deny_effect_rejected",
			effect:     authz.EffectDeny,
			conditions: []abac.Condition{frozenOwnerCondition},
			wantErr:    true,
		},
		{
			name:       "extra_cross_attr_tenant_condition_rejected",
			effect:     authz.EffectAllow,
			conditions: []abac.Condition{frozenOwnerCondition, tenantCrossAttr},
			wantErr:    true,
		},
		{
			name:   "static_tenant_match_rejected",
			effect: authz.EffectAllow,
			conditions: []abac.Condition{{
				Source: abac.SourceSubject, Key: "tenant_id", Operator: abac.OpEquals,
				Values: []string{"00000000-0000-0000-0000-000000000001"},
			}},
			wantErr: true,
		},
		{
			name:       "cross_attr_tenant_match_rejected",
			effect:     authz.EffectAllow,
			conditions: []abac.Condition{tenantCrossAttr},
			wantErr:    true,
		},
		{
			name:   "drifted_rhskey_to_tenant_rejected",
			effect: authz.EffectAllow,
			conditions: []abac.Condition{{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: "tenant_id",
			}},
			wantErr: true,
		},
		{
			name:       "zero_conditions_rejected",
			effect:     authz.EffectAllow,
			conditions: []abac.Condition{},
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkOwnerRuleFrozen(abac.Rule{ID: "synthetic-" + tc.name, Effect: tc.effect, Conditions: tc.conditions})
			switch {
			case tc.wantErr && err == nil:
				t.Errorf("checkOwnerRuleFrozen must REJECT %s — the freeze would be vacuous otherwise", tc.name)
			case !tc.wantErr && err != nil:
				t.Errorf("checkOwnerRuleFrozen must ACCEPT the exact frozen ownership shape, got: %v", err)
			}
		})
	}
}
