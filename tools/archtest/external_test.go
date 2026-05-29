package archtest

// INVARIANT: (none — exercises the importable external surface in external.go)
//
// external_test.go covers the importable library entry points an external Cell
// repository uses: [CellRule] / [StandardCellRules] / [RunStandardCellRules]
// (the go/analysis Analyzer+multichecker analog) and the ExtraRules plugin.

import "testing"

// TestStandardCellRulesComposition asserts the curated set is well-formed:
// non-empty, every rule has a non-empty unique ID and a non-nil Run, and the
// PR-1 exemplar PANIC-REGISTERED-01 is present. Cheap (no module scan).
func TestStandardCellRulesComposition(t *testing.T) {
	t.Parallel()
	rules := StandardCellRules()
	if len(rules) == 0 {
		t.Fatal("StandardCellRules() must not be empty")
	}
	seen := make(map[string]bool, len(rules))
	var hasPanicRule bool
	for _, r := range rules {
		if r == nil {
			t.Fatal("StandardCellRules() contains a nil *CellRule")
		}
		if r.ID == "" {
			t.Error("StandardCellRules() contains a rule with an empty ID")
		}
		if r.Run == nil {
			t.Errorf("rule %q has a nil Run", r.ID)
		}
		if seen[r.ID] {
			t.Errorf("duplicate rule ID %q in StandardCellRules()", r.ID)
		}
		seen[r.ID] = true
		if r.ID == rulePanicRegistered01 {
			hasPanicRule = true
		}
	}
	if !hasPanicRule {
		t.Errorf("StandardCellRules() must include %q (PR-1 exemplar)", rulePanicRegistered01)
	}
}

// TestRunStandardCellRules dogfoods the aggregate entry against GoCell itself:
// the curated set must pass (GoCell is compliant), and an ExtraRules probe must
// be invoked (proving the minimal plugin surface is wired). It re-runs the
// PANIC-REGISTERED-01 scan, which reuses the warmed SharedResolver cache, but
// is still gated behind -short to keep the fast lane lean.
func TestRunStandardCellRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-wide RunStandardCellRules dogfood in -short mode")
	}

	extraInvoked := false
	probe := &CellRule{
		ID: "TEST-EXTRA-PROBE-01",
		Run: func(_ *testing.T, _ ConfigForExternalCell) []Diagnostic {
			extraInvoked = true
			return nil // no diagnostics → Report is a clean pass
		},
	}

	// GoCell's own production composition root; a passing standard set here is
	// the in-repo proof that the external entry works end-to-end.
	RunStandardCellRules(t, ConfigForExternalCell{
		ProductionMainPkgs: []string{"./cmd/corebundle"},
		ExtraRules:         []*CellRule{probe},
	})

	if !extraInvoked {
		t.Error("RunStandardCellRules did not invoke the ExtraRules custom rule")
	}
}

// TestRunStandardCellRulesSkipsNilExtraRule asserts a nil entry in ExtraRules
// (or a rule with a nil Run) is skipped rather than panicking — defensive for
// consumers assembling rule slices dynamically. Cheap: the nil ExtraRule adds
// nothing, but the standard set still runs, so gate behind -short.
func TestRunStandardCellRulesSkipsNilExtraRule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-wide RunStandardCellRules dogfood in -short mode")
	}
	// Must not panic.
	RunStandardCellRules(t, ConfigForExternalCell{
		ExtraRules: []*CellRule{nil, {ID: "TEST-NIL-RUN-01", Run: nil}},
	})
}
