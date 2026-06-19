package authorizationdecide

// INVARIANT: BASELINE-ALLOW-ACTION-NONEMPTY-01
//
// BASELINE-ALLOW-ACTION-NONEMPTY-01 freezes a single property over the built-in
// baseline rules: every EffectAllow rule declares a non-empty Action. It is the
// static-surface half of the #1979 defense — "a tenant allow rule must name the
// route gate(s) it widens" — applied to the platform's OWN rules.
//
// # Why
//
// An allow rule with an empty Action (combined with empty Conditions) permits
// EVERY action unconditionally — one such rule blows open the route gate for all
// permissions. #1979 enforces non-empty Action for allows on three surfaces:
//
//   - tenant rules (runtime wire input) → abac.Rule.Validate, write-side 422;
//   - any rule that slips past validation (e.g. a legacy persisted policy) →
//     the evaluator's read-side fail-closed (applyRule treats an empty-Action
//     allow as not-applicable);
//   - the baseline rules (static Go values) → THIS freeze.
//
// The baseline rules are constructed in Go and never pass through Rule.Validate
// at runtime, so the write-side guard does not cover them. Without this test a
// future edit could add an empty-Action baseline allow and silently widen every
// gate; here it trips the build-test lane.
//
// # AI-robust grade: Medium (value-level, same package)
//
// The test calls builtinBaselineRules() (unexported — hence a same-package
// internal test, not a tools/archtest scan) and asserts the property over the
// real constructed rules the evaluator consumes. This is the same grade and
// shape as the sibling BASELINE-OWNER-RULE-TENANT-FREEZE-01 in this package. A
// runtime non-empty-slice predicate is not expressible at Go compile time, so a
// genuine Hard (unrepresentable) guard is not reachable for this invariant; the
// Hard-ification path is the shared PR-13 end state — baseline rules derived
// from a metadata/contract declaration + codegen + byte golden — at which point
// this test becomes redundant and is removed.
//
// # Anti-vacuity
//
// TestBaselineAllowActionNonEmpty_01 fails if builtinBaselineRules() contains no
// EffectAllow rule at all (a refactor that empties/renames the set would
// otherwise make the property vacuously green).
// TestBaselineAllowActionNonEmpty_01_RejectsEmptyActionAllow feeds the predicate
// a crafted empty-Action allow (synthetic RED) and asserts it is rejected, plus
// controls that an empty-Action deny (deny-all) and a scoped allow are accepted —
// proving the check can actually fail and is correctly allow-specific.
//
// # Blind spots
//
//   - Scope is the baseline set only. Tenant rules are covered by Rule.Validate
//     (write) + evaluator read-side fail-closed (read), not by this test.
//   - The property is "non-empty Action", not "Action ∈ the closed permission
//     registry": a baseline allow targeting a typo'd permission would pass here
//     (it would simply never match a real action — an inert dead rule, not a
//     widening). Registry membership is out of #1979 scope.

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
)

// baselineAllowActionNonEmpty reports whether the rule satisfies the freeze: an
// EffectAllow rule must carry a non-empty Action; a Deny rule is exempt (empty
// Action = deny-all). It is the predicate enforced over the real baseline rules
// and is also fed synthetic shapes below to prove it can fail (anti-vacuity).
func baselineAllowActionNonEmpty(r abac.Rule) bool {
	if r.Effect != authz.EffectAllow {
		return true
	}
	return len(r.Action) > 0
}

func TestBaselineAllowActionNonEmpty_01(t *testing.T) {
	var allowSeen int
	for _, r := range builtinBaselineRules() {
		if r.Effect == authz.EffectAllow {
			allowSeen++
		}
		if !baselineAllowActionNonEmpty(r) {
			t.Errorf("baseline rule %q is EffectAllow with empty Action (#1979): an allow rule "+
				"must declare at least one Action, else it permits every action and blows open "+
				"the route gate for all permissions", r.ID)
		}
	}
	if allowSeen == 0 {
		t.Fatal("anti-vacuity: builtinBaselineRules() returned no EffectAllow rules; the freeze would be vacuous")
	}
}

func TestBaselineAllowActionNonEmpty_01_RejectsEmptyActionAllow(t *testing.T) {
	// Synthetic RED: an empty-Action allow must be rejected by the predicate.
	bad := abac.Rule{ID: "synthetic-bad", Name: "bad", Effect: authz.EffectAllow}
	if baselineAllowActionNonEmpty(bad) {
		t.Fatal("predicate must reject an EffectAllow rule with empty Action")
	}
	// Control: an empty-Action deny is a legitimate deny-all and must be accepted.
	denyAll := abac.Rule{ID: "deny-all", Name: "deny", Effect: authz.EffectDeny}
	if !baselineAllowActionNonEmpty(denyAll) {
		t.Fatal("predicate must accept an empty-Action Deny rule (deny-all)")
	}
	// Control: a scoped allow must be accepted.
	scoped := abac.Rule{ID: "scoped", Name: "ok", Effect: authz.EffectAllow, Action: []string{"audit:read"}}
	if !baselineAllowActionNonEmpty(scoped) {
		t.Fatal("predicate must accept an Allow rule with non-empty Action")
	}
}
