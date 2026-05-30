package archtest

// INVARIANT: ARCHTEST-EXTERNAL-SURFACE-01
//
// external_test.go covers the importable library entry points an external Cell
// repository uses: [CellRule] / [StandardCellRules] / [RunStandardCellRules]
// (the go/analysis Analyzer+multichecker analog) and the ExtraRules plugin.
// ARCHTEST-EXTERNAL-SURFACE-01 is a unit-coverage anchor for the external
// façade (same convention as ARCHTEST-PASS-DRIVER-UNIT-01 / GOLDEN-HELPER-UNIT-01),
// not a production invariant gate.

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

	// A passing standard set against GoCell itself is the in-repo proof that the
	// external entry works end-to-end; BuildTags exercises the second scan pass.
	RunStandardCellRules(t, ConfigForExternalCell{
		BuildTags:  FlatNonDefaultTags(),
		ExtraRules: []*CellRule{probe},
	})

	if !extraInvoked {
		t.Error("RunStandardCellRules did not invoke the ExtraRules custom rule")
	}
}

// TestValidateCellRule asserts a misconfigured ExtraRule (nil, nil Run, or
// empty ID) is rejected loud — never silently skipped — so a mis-wired rule
// slice cannot green-light a run that gated nothing. It tests the pure
// validateCellRule helper directly (RunStandardCellRules wraps it with
// t.Errorf), which is cheap and needs no module scan or *testing.T interception.
func TestValidateCellRule(t *testing.T) {
	t.Parallel()
	nonNilRun := func(_ *testing.T, _ ConfigForExternalCell) []Diagnostic { return nil }
	cases := []struct {
		name    string
		r       *CellRule
		wantErr bool
	}{
		{"nil-rule", nil, true},
		{"nil-run", &CellRule{ID: "X"}, true},
		{"empty-id", &CellRule{ID: "", Run: nonNilRun}, true},
		{"valid", &CellRule{ID: "X", Run: nonNilRun}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if gotErr := validateCellRule(c.r, 0) != ""; gotErr != c.wantErr {
				t.Errorf("validateCellRule(%s) error = %v, want %v", c.name, gotErr, c.wantErr)
			}
		})
	}
}
