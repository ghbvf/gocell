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
// non-empty, every rule has a non-empty unique ID and a non-nil Run, and its
// membership is EXACTLY the adjudicated set. Cheap (no module scan).
//
// Asserting exact membership (not merely "contains PANIC-REGISTERED-01") proves
// the standard set holds only adjudicated rules: registering a new rule or
// dropping one becomes a deliberate, test-visible edit to wantRuleIDs rather
// than silent drift. This closes codex #1621 F1 — the cell-family rules are
// gocell-internal and deliberately NOT registered (see each rule file's godoc),
// so a stray registration must fail here, not slip in behind a stale "wrapped
// by StandardCellRules" narrative.
func TestStandardCellRulesComposition(t *testing.T) {
	t.Parallel()
	rules := StandardCellRules()
	if len(rules) == 0 {
		t.Fatal("StandardCellRules() must not be empty")
	}
	seen := make(map[string]bool, len(rules))
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
	}

	// wantRuleIDs is the adjudicated standard set. PR-1 ships PANIC-REGISTERED-01
	// as the sole migration exemplar; the cell-family rules are gocell-internal
	// and NOT registered. A new entry here must be a rule portable to external
	// Cell repos (it reasons about platform-API usage, not GoCell's own package
	// layout) — see external.go's StandardCellRules godoc.
	wantRuleIDs := map[string]bool{rulePanicRegistered01: true}
	for id := range seen {
		if !wantRuleIDs[id] {
			t.Errorf("StandardCellRules() contains undeclared rule %q; if intended, add it to "+
				"wantRuleIDs and confirm the rule is portable to external Cell repos", id)
		}
	}
	for id := range wantRuleIDs {
		if !seen[id] {
			t.Errorf("StandardCellRules() is missing adjudicated rule %q", id)
		}
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
