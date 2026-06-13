package authorizationdecide

// INVARIANT: BASELINE-OWNER-RULE-TENANT-FREEZE-01
//
// BASELINE-OWNER-RULE-TENANT-FREEZE-01 freezes the baseline grant surface of the
// owner-scoped actions (user:read, user:write, role:read) so that tenant can never become
// an owner-grant factor. It enforces two complementary properties:
//
//  1. Each owner-scoped SELF rule (baseline-user-read-self, baseline-user-write-self,
//     baseline-role-read-self) is an EffectAllow rule granting EXACTLY its one action and
//     carrying EXACTLY the ownership condition subject.sub == resource.id (Source=SourceSubject,
//     Key="sub", Operator=OpEqualsAttr, RHSSource=SourceResource, RHSKey="id", no static Values).
//  2. The CLOSED SET of EffectAllow rules granting each owner-scoped action is EXACTLY two —
//     the one frozen owner rule (self access) and the one admin rule (subject.roles ∈
//     {admin, super-admin}) — and nothing else.
//
// # Threat closed — the real owner→tenant widening vectors
//
// #1977 moved accesscore self/ownership into the PDP via these baseline rules. Correctness
// rests on the ownership condition comparing request-local UUIDs — it is tenant-AGNOSTIC, so
// a cross-tenant subject (a different tenant_id claim) is denied exactly like a same-tenant
// non-owner (proven end-to-end by TestABACPDPGatesAccesscore's cross_tenant_* cases, #2026).
// This freeze locks the policy REPRESENTATION so that invariant cannot silently erode.
//
// Because the PDP evaluates rules with OR semantics ACROSS rules and AND semantics WITHIN a
// rule's Conditions, the actual ways to widen owner-scoped access to tenant-scoped are:
//
//   - Adding a SEPARATE tenant-matching allow rule on an owner action (e.g. a rule with
//     subject.tenant_id == resource.tenant_id) — ORs into the decision and grants any
//     same-tenant subject. THIS is the primary widening vector, caught by property (2).
//   - Replacing the owner rule's condition with a tenant condition — caught by property (1).
//   - Broadening an owner rule's Action to additional permissions — caught by property (1).
//
// NOTE (corrects an earlier misframing, PR #2075 review F2): adding an EXTRA condition to an
// existing owner rule does NOT widen — AND-combination TIGHTENS the rule (it fires less
// often). It is still rejected by property (1) as forbidden drift from the single frozen
// ownership condition, but it is not a widening; the widening vectors are the three above.
// A tenant edit that widens always shows up as a SEPARATE allow rule or a replaced
// condition/action, never as an extra AND-condition on the existing rule.
//
// All three vectors bypass the principal-derived RowScope tenant isolation that the route
// gate is a defense-in-depth layer over (not a replacement for); any of them changes a
// rule's fields or the action's allow-rule set and trips this test in the build-test lane.
//
// # AI-robust grade: Medium (value-level golden, same package)
//
// The test calls builtinBaselineRules() (unexported — hence a same-package internal test,
// not a tools/archtest scan) and asserts the two properties over the real constructed rules.
// frozenOwnerCondition / frozenAdminCondition are spelled as INDEPENDENT literals (NOT
// derived from subjectIsResource() / adminOrSuperAdmin()) precisely so a change to those
// helpers is caught as drift rather than silently tracked. A value-golden over the real
// constructed rules is stronger than an AST scan here: it compares the exact structs the
// evaluator consumes, with no literal-parsing fragility. Property (2)'s closed set is what
// makes this stronger than naming the three self-rule IDs alone — it catches a NEW allow rule
// on an owner action even if that rule has an ID the test never enumerated. Hard-ification
// path: when baseline rules are derived from a metadata/contract declaration + codegen + byte
// golden (the PR-13 end state tracked for OWNER-SCOPED-GATE-EXACT-SET-01 /
// PERMISSION-BASED-AUTHZ-01), this test becomes redundant and is removed.
//
// # Anti-vacuity
//
// TestBaselineOwnerRuleTenantFreeze_01 looks each owner self-rule ID up in
// builtinBaselineRules() and fails if absent (a vacuous green from renamed/removed rules
// trips the "not found" check), and runs the closed-set check against the real baseline.
// TestBaselineOwnerRuleTenantFreeze_01_RejectsForbiddenShapes feeds crafted drift shapes
// through checkOwnerRuleFrozen, and TestBaselineOwnerActionSurface_RejectsExtraAllowRule
// injects a SEPARATE tenant-matching allow rule and asserts checkOwnerActionSurfaceClosed
// rejects it — proving both properties can actually fail.
//
// # Blind spots
//
//   - The owner-scoped action set (the keys' values in ownerScopedBaselineSelfRules) is
//     hand-maintained: a NEW owner-scoped action/permission must be registered there or its
//     grant surface is not frozen. This is a smaller surface than enumerating rule IDs
//     (property (2) freezes the whole allow-rule set per known action), but a brand-new owner
//     ACTION still needs a one-line registration.
//   - Freezes the grant surface of owner-scoped actions only; OWNER-SCOPED-GATE-EXACT-SET-01
//     separately freezes the route-gate (handler|param|perm) set. Deny rules are not in scope
//     (a stray deny narrows, never widens, and would surface via the e2e ownership-200 cases).

import (
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	runtimeauth "github.com/ghbvf/gocell/runtime/auth"
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

// frozenAdminCondition is the admin/super-admin role condition — subject.roles ∈
// {admin, super-admin} — the ONLY other sanctioned condition shape allowed to grant an
// owner-scoped action. Spelled independently of adminOrSuperAdmin() (same drift-catch
// rationale as frozenOwnerCondition). The closed-set check (property 2) recognizes exactly
// these two shapes; anything else granting an owner action is rejected as a third shape.
var frozenAdminCondition = abac.Condition{
	Source:   abac.SourceSubject,
	Key:      "roles",
	Operator: abac.OpIn,
	Values:   []string{runtimeauth.RoleAdmin, runtimeauth.RoleSuperAdmin},
}

// ownerScopedBaselineSelfRules maps each owner-scoped SELF rule ID to the single action it
// must grant. The freeze asserts each named rule is EffectAllow, grants EXACTLY that one
// action (no broadening), and carries EXACTLY the frozen owner condition. Its values are
// also the closed set of owner-scoped actions whose entire allow surface is frozen.
var ownerScopedBaselineSelfRules = map[string]string{
	"baseline-user-read-self":  authz.PermUserRead().String(),
	"baseline-user-write-self": authz.PermUserWrite().String(),
	"baseline-role-read-self":  authz.PermRoleRead().String(),
}

// conditionMatches reports field-by-field equality of two abac.Conditions, including the
// Values slice (which makes abac.Condition non-comparable with ==). Order-sensitive on
// Values — a reordered admin role list is itself a drift worth surfacing.
func conditionMatches(got, want abac.Condition) bool {
	if got.Source != want.Source || got.Key != want.Key || got.Operator != want.Operator ||
		got.RHSSource != want.RHSSource || got.RHSKey != want.RHSKey || len(got.Values) != len(want.Values) {
		return false
	}
	for i := range got.Values {
		if got.Values[i] != want.Values[i] {
			return false
		}
	}
	return true
}

// ruleGrantsAction reports whether r's Action set grants action. An EMPTY Action is
// untargeted (per abac.Rule godoc it applies to every action), so it grants action too —
// and would surface in checkOwnerActionSurfaceClosed as an unexpected rule, which is correct:
// a baseline allow rule that grants an owner-scoped action via an untargeted Action is itself
// a grant-surface anomaly.
func ruleGrantsAction(r abac.Rule, action string) bool {
	if len(r.Action) == 0 {
		return true
	}
	for _, a := range r.Action {
		if a == action {
			return true
		}
	}
	return false
}

// checkOwnerRuleFrozen returns nil iff r is an EffectAllow rule granting EXACTLY wantAction
// and carrying EXACTLY the frozen ownership condition. A non-nil error names the deviation.
func checkOwnerRuleFrozen(r abac.Rule, wantAction string) error {
	if r.Effect != authz.EffectAllow {
		return fmt.Errorf("rule %q: Effect = %v, want EffectAllow — an owner-scoped rule that is not an "+
			"ALLOW grant (e.g. flipped to Deny) is not the grant shape this invariant freezes", r.ID, r.Effect)
	}
	if len(r.Action) != 1 || r.Action[0] != wantAction {
		return fmt.Errorf("rule %q: Action = %v, want exactly [%q] — broadening an owner rule's granted "+
			"action set widens owner-scoped access to additional permissions", r.ID, r.Action, wantAction)
	}
	if len(r.Conditions) != 1 {
		return fmt.Errorf("rule %q: got %d conditions, want exactly 1 (the ownership condition) — the frozen "+
			"grant shape is a single ownership condition; an extra AND-condition is forbidden drift (it would "+
			"TIGHTEN this rule, not widen — real owner→tenant widening comes from a SEPARATE tenant-matching "+
			"allow rule, caught by checkOwnerActionSurfaceClosed)", r.ID, len(r.Conditions))
	}
	if !conditionMatches(r.Conditions[0], frozenOwnerCondition) {
		return fmt.Errorf("rule %q: condition = %+v, want %+v (subject.sub == resource.id, no static Values) — "+
			"replacing the owner condition (e.g. with Key/RHSKey \"tenant_id\", or a static tenant Values set) "+
			"changes the grant surface from owner-scoped to tenant-scoped", r.ID, r.Conditions[0], frozenOwnerCondition)
	}
	return nil
}

// checkOwnerActionSurfaceClosed returns nil iff the EffectAllow rules in rules that grant
// action form EXACTLY the frozen surface: one carrying the frozen owner condition and one
// carrying the frozen admin condition, and nothing else. A third allow rule on the same
// action — the primary owner→tenant widening vector, since the PDP ORs allow rules — fails
// here (its condition matches neither frozen shape → other, or it pushes a count above 1).
func checkOwnerActionSurfaceClosed(rules []abac.Rule, action string) error {
	var owner, admin, other int
	for _, r := range rules {
		if r.Effect != authz.EffectAllow || !ruleGrantsAction(r, action) {
			continue
		}
		switch {
		case len(r.Conditions) == 1 && conditionMatches(r.Conditions[0], frozenOwnerCondition):
			owner++
		case len(r.Conditions) == 1 && conditionMatches(r.Conditions[0], frozenAdminCondition):
			admin++
		default:
			other++
		}
	}
	if owner != 1 || admin != 1 || other != 0 {
		return fmt.Errorf("action %q baseline allow surface = {owner:%d, admin:%d, other:%d}, want {1,1,0} — "+
			"the only allow rules granting an owner-scoped action may be the single frozen owner rule and the "+
			"single admin rule; any additional allow rule (e.g. a tenant-matching grant) ORs into the decision "+
			"and widens owner-scoped access to tenant-scoped", action, owner, admin, other)
	}
	return nil
}

// TestBaselineOwnerRuleTenantFreeze_01 enforces BASELINE-OWNER-RULE-TENANT-FREEZE-01 over the
// real baseline: each owner self-rule is frozen (Effect + exact Action + owner condition) and
// each owner-scoped action's full allow surface is the closed {owner, admin} set.
func TestBaselineOwnerRuleTenantFreeze_01(t *testing.T) {
	rules := builtinBaselineRules()
	byID := make(map[string]abac.Rule, len(rules))
	for _, r := range rules {
		byID[r.ID] = r
	}

	// Property (1): each named owner self-rule is exactly the frozen grant shape.
	for id, wantAction := range ownerScopedBaselineSelfRules {
		r, ok := byID[id]
		if !ok {
			t.Errorf("BASELINE-OWNER-RULE-TENANT-FREEZE-01: owner self-rule %q not found in "+
				"builtinBaselineRules() — removed or renamed without updating ownerScopedBaselineSelfRules "+
				"(anti-vacuity: the freeze must inspect every real owner self-rule)", id)
			continue
		}
		if err := checkOwnerRuleFrozen(r, wantAction); err != nil {
			t.Errorf("BASELINE-OWNER-RULE-TENANT-FREEZE-01: %v", err)
		}
	}

	// Property (2): each owner-scoped action's full allow surface is the closed {owner, admin} set.
	for _, action := range ownerScopedBaselineSelfRules {
		if err := checkOwnerActionSurfaceClosed(rules, action); err != nil {
			t.Errorf("BASELINE-OWNER-RULE-TENANT-FREEZE-01: %v", err)
		}
	}
}

// TestBaselineOwnerRuleTenantFreeze_01_RejectsForbiddenShapes proves property (1) is
// non-vacuous: crafted forbidden shapes (Deny, broadened action, replaced/extra condition)
// are each REJECTED by checkOwnerRuleFrozen, and the exact frozen shape ACCEPTED.
func TestBaselineOwnerRuleTenantFreeze_01_RejectsForbiddenShapes(t *testing.T) {
	const wantAction = "user:read"
	// A cross-attr tenant condition: replacing the owner condition with this (or adding it as
	// a separate allow rule) is the widening; as an extra AND-condition it merely tightens.
	tenantCrossAttr := abac.Condition{
		Source: abac.SourceSubject, Key: "tenant_id", Operator: abac.OpEqualsAttr,
		RHSSource: abac.SourceResource, RHSKey: "tenant_id",
	}
	tests := []struct {
		name       string
		effect     authz.Effect
		action     []string
		conditions []abac.Condition
		wantErr    bool
	}{
		{
			name:       "frozen_ownership_shape_accepted",
			effect:     authz.EffectAllow,
			action:     []string{wantAction},
			conditions: []abac.Condition{frozenOwnerCondition},
			wantErr:    false,
		},
		{
			name:       "deny_effect_rejected",
			effect:     authz.EffectDeny,
			action:     []string{wantAction},
			conditions: []abac.Condition{frozenOwnerCondition},
			wantErr:    true,
		},
		{
			name:       "broadened_action_rejected",
			effect:     authz.EffectAllow,
			action:     []string{wantAction, "policy:read"},
			conditions: []abac.Condition{frozenOwnerCondition},
			wantErr:    true,
		},
		{
			name:       "replaced_with_cross_attr_tenant_rejected",
			effect:     authz.EffectAllow,
			action:     []string{wantAction},
			conditions: []abac.Condition{tenantCrossAttr},
			wantErr:    true,
		},
		{
			name:   "replaced_with_static_tenant_match_rejected",
			effect: authz.EffectAllow,
			action: []string{wantAction},
			conditions: []abac.Condition{{
				Source: abac.SourceSubject, Key: "tenant_id", Operator: abac.OpEquals,
				Values: []string{"00000000-0000-0000-0000-000000000001"},
			}},
			wantErr: true,
		},
		{
			name:   "drifted_rhskey_to_tenant_rejected",
			effect: authz.EffectAllow,
			action: []string{wantAction},
			conditions: []abac.Condition{{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: "tenant_id",
			}},
			wantErr: true,
		},
		{
			// Forbidden drift (not a widening — AND-combination tightens), still rejected.
			name:       "extra_and_condition_rejected",
			effect:     authz.EffectAllow,
			action:     []string{wantAction},
			conditions: []abac.Condition{frozenOwnerCondition, tenantCrossAttr},
			wantErr:    true,
		},
		{
			name:       "zero_conditions_rejected",
			effect:     authz.EffectAllow,
			action:     []string{wantAction},
			conditions: []abac.Condition{},
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkOwnerRuleFrozen(
				abac.Rule{ID: "synthetic-" + tc.name, Effect: tc.effect, Action: tc.action, Conditions: tc.conditions},
				wantAction)
			switch {
			case tc.wantErr && err == nil:
				t.Errorf("checkOwnerRuleFrozen must REJECT %s — the freeze would be vacuous otherwise", tc.name)
			case !tc.wantErr && err != nil:
				t.Errorf("checkOwnerRuleFrozen must ACCEPT the exact frozen ownership shape, got: %v", err)
			}
		})
	}
}

// TestBaselineOwnerActionSurface_RejectsExtraAllowRule proves property (2) is non-vacuous:
// the real baseline satisfies the closed set, but injecting a SEPARATE tenant-matching allow
// rule on an owner action (the primary widening vector) makes checkOwnerActionSurfaceClosed
// fail.
func TestBaselineOwnerActionSurface_RejectsExtraAllowRule(t *testing.T) {
	action := authz.PermUserRead().String()

	if err := checkOwnerActionSurfaceClosed(builtinBaselineRules(), action); err != nil {
		t.Errorf("real baseline must satisfy the closed allow surface for %q: %v", action, err)
	}

	tenantAllow := abac.Rule{
		ID:     "synthetic-tenant-allow-user-read",
		Effect: authz.EffectAllow,
		Action: []string{action},
		Conditions: []abac.Condition{{
			Source: abac.SourceSubject, Key: "tenant_id", Operator: abac.OpEqualsAttr,
			RHSSource: abac.SourceResource, RHSKey: "tenant_id",
		}},
	}
	widened := append(append([]abac.Rule{}, builtinBaselineRules()...), tenantAllow)
	if err := checkOwnerActionSurfaceClosed(widened, action); err == nil {
		t.Errorf("checkOwnerActionSurfaceClosed must REJECT a baseline with an extra tenant-matching allow "+
			"rule on %q (the real owner→tenant widening vector) — the closed-set check would be vacuous otherwise",
			action)
	}
}
