package archtest

// INVARIANT: TAGGROUP-LOOP-FORBIDS-RUNTYPED-01

import (
	"testing"
)

// TestTagGroupLoopForbidsRunTyped enforces TAGGROUP-LOOP-FORBIDS-RUNTYPED-01
// against production archtest *_test.go files. Rule logic lives in
// taggroup_loop_no_runtyped_invariants.go; this file dogfoods it — single
// source, no parallel rule body.
//
// F3 cacheKey note: this scan's cacheKey is (modRoot, Tests=true, nil,
// "./tools/archtest/...") — deliberately NOT the (modRoot, false, nil,
// "./...") key TestMain warms. The amortization in ADR 202605190000 does not
// cover this rule's own scan; it pays one cold packages.Load. That is
// accepted (a single test, scope is bounded to one package). If it lands
// borderline on the 20s slowgate post-merge, that is expected behavior, not
// a regression — it is tracked under the slowgate cleanup
// (ARCHTEST-SLOWGATE-ALLOWLIST-CLEANUP-01, subprocess/cache-miss triage).
func TestTagGroupLoopForbidsRunTyped(t *testing.T) {
	t.Parallel()
	Report(t, taggroupLoopRuleID,
		CheckTagGroupLoopForbidsRunTyped(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// Test_TaggroupLoopFixturePrecisionGate verifies the rule's accuracy against
// the curated red/green fixture set: every red_*.go must trigger at least
// one diagnostic; no green_*.go may trigger any.
//
// Note: TypedOpts{} (Tests=false) here differs intentionally from the
// live scan's TypedOpts{Tests: true}. Fixture files use anonymous
// `func _(...)` declarations (not Test* entries), so test-variant mode
// is unnecessary; the underlying *types.Info pipeline is the same.
func Test_TaggroupLoopFixturePrecisionGate(t *testing.T) {
	t.Parallel()

	diags := Run(t, Typed(TypedOpts{}, []string{taggroupLoopPatternFixture}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				out = append(out, scanFileForTaggroupViolations(p, file, rel)...)
			}
			return out
		})

	hits := make(map[string]int)
	for _, d := range diags {
		base := basenameWithoutExt(d.Rel)
		hits[base]++
	}

	expectedRed := []string{
		"red_aliased_import",
		"red_nested_closure",
		"red_panic_invariants_style",
		"red_subpath_runtyped",
		"red_typeseval_qualified",
		"red_var_bound_range",
		// Per-member typed-scope trip-wires (F3): each locks one entry of
		// taggroupTypedScopeCtors so dropping it from the set fails CI.
		"red_production_scope",
		"red_fixture_scope",
		"red_standalone_module_scope",
		// F2 scope-var-indirection form: scope := Typed(...); Run(t, scope, ...).
		"red_scope_var_indirection",
	}
	for _, name := range expectedRed {
		if hits[name] == 0 {
			t.Errorf("RED fixture %s.go: expected to be caught, but the rule produced no diagnostic for it", name)
		}
	}

	disallowedGreen := []string{
		"green_single_load_production_flat",
		"green_two_loads_nil_and_flat",
		"green_var_bound_parity",
	}
	for _, name := range disallowedGreen {
		if hits[name] > 0 {
			t.Errorf("GREEN fixture %s.go: unexpectedly caught by rule (%d diagnostic(s)) — false positive", name, hits[name])
		}
	}
}
