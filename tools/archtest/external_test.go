//go:build archtest

package archtest

// INVARIANT: ARCHTEST-EXTERNAL-SURFACE-01
//
// external_test.go covers the importable library entry points an external Cell
// repository uses: [CellRule] / [StandardCellRules] / [RunStandardCellRules]
// (the go/analysis Analyzer+multichecker analog) and the ExtraRules plugin.
// ARCHTEST-EXTERNAL-SURFACE-01 is a unit-coverage anchor for the external
// façade (same convention as ARCHTEST-PASS-DRIVER-UNIT-01 / GOLDEN-HELPER-UNIT-01),
// not a production invariant gate.

import (
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/scoperules"
	"github.com/ghbvf/gocell/tools/gomodutil"
)

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

	// wantRuleIDs is the adjudicated standard set, derived from the single source
	// scoperules.FrameworkRuleIDs (#2331). StandardCellRules() itself iterates that
	// same leaf, so this assertion proves the derivation kept membership intact and
	// no rule was paired with a nil Run. Adding/dropping a framework rule is a
	// single-line edit to scoperules.FrameworkRuleIDs; the runner (gocell verify
	// archtest --scope=framework) reads the identical leaf, so the two cannot drift.
	frameworkIDs := scoperules.FrameworkRuleIDs()
	wantRuleIDs := make(map[string]bool, len(frameworkIDs))
	for _, id := range frameworkIDs {
		wantRuleIDs[id] = true
	}
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
	// external entry works end-to-end; BuildTags exercises the second scan pass and
	// ProductionMainPkgs exercises PROD-MAIN-WIRING-NOOP-REJECT-01 against GoCell's
	// own composition root (cmd/corebundle) through the aggregate entry.
	RunStandardCellRules(t, ConfigForExternalCell{
		BuildTags:          FlatNonDefaultTags(),
		ProductionMainPkgs: []string{"./cmd/corebundle"},
		ExtraRules:         []*CellRule{probe},
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

// TestPlatformModulePathMatchesGoMod guards the one hardcoded module path — the
// PlatformModulePath const — against the go.mod module declaration that
// moduleImportPath reads dynamically. They MUST be equal: every funnel that
// derives a platform symbol path as PlatformModulePath+"/…" compares it against
// types.Pkg().Path() from the loaded packages. If the const drifted (module
// rename / a /v2 bump) it would never match, so every such scan would silently
// go vacuous-green AND its RED fixtures (which build paths from the SAME const)
// would also pass — zero CI signal across the whole module-path-agnostic funnel
// family. This is the assertion moduleImportPath's own godoc presumes when it
// warns that "hardcoding the path would silently mis-resolve on a module rename".
func TestPlatformModulePathMatchesGoMod(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Anchor: the core framework module's go.mod (root/framework, post-#1565).
	// PlatformFrameworkModulePath MUST equal it — every framework symbol path is
	// derived as PlatformFrameworkModulePath+"/kernel/…", compared against
	// types.Pkg().Path() from loaded packages; a drift would make every such scan
	// silently vacuous-green (and its RED fixtures, built from the same const, also
	// pass) — zero CI signal across the whole module-path-agnostic funnel family.
	fwGot, err := gomodutil.ReadModulePath(filepath.Join(root, frameworkSubdir))
	if err != nil {
		t.Fatalf("read framework/go.mod module path: %v", err)
	}
	if fwGot != PlatformFrameworkModulePath {
		t.Fatalf("PlatformFrameworkModulePath = %q, but framework/go.mod declares module %q; "+
			"the const must track go.mod or every framework symbol-path scan goes vacuous-green",
			PlatformFrameworkModulePath, fwGot)
	}

	// Cross-check the org-prefix derivation: moduleImportPath strips "/framework"
	// off the anchor and must land on PlatformModulePath (the sibling-module prefix).
	got, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("resolve org prefix: %v", err)
	}
	if got != PlatformModulePath {
		t.Fatalf("PlatformModulePath = %q, but resolved org prefix is %q (framework module %q "+
			"minus /framework)", PlatformModulePath, got, fwGot)
	}
}
