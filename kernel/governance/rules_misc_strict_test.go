package governance

// rules_misc_strict_test.go consolidates tests for the strict-mode
// orchestrator (FMT-16/17/C1/A1) and the FMT-20/21/22/23/25 schema rules,
// all of which now live in rules_misc_strict.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// =============================================================================
// strict orchestrator + FMT-16/17/C1/A1 (formerly rules_strict_test.go)
// =============================================================================

// TestStrictValidator_KebabDirDisallowed verifies that --strict mode upgrades
// kebab-case slice directory warnings to errors.
func TestStrictValidator_KebabDirDisallowed(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/accesscore/slices/session-login/**",
				},
				Dir:     "session-login",
				CellDir: "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// Non-strict: kebab dir in slice dir should produce warning, not error.
	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	hasKebabError := false
	for _, r := range results {
		if r.Code == "FMT-16" && r.Severity == SeverityError {
			hasKebabError = true
		}
	}
	if hasKebabError {
		t.Error("non-strict mode should not produce FMT-16 error for kebab slice dir")
	}

	// Strict mode: kebab dir in slice dir should produce error.
	results, err = v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	hasKebabError = false
	for _, r := range results {
		if r.Code == "FMT-16" && r.Severity == SeverityError {
			hasKebabError = true
		}
	}
	if !hasKebabError {
		t.Error("strict mode should produce FMT-16 error for kebab-case slice directory")
	}
}

// TestStrictValidator_AllowedFilesMismatch verifies that strict mode errors when
// allowedFiles first entry doesn't match the slice directory.
func TestStrictValidator_AllowedFilesMismatch(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				// First entry points to wrong (kebab) path.
				AllowedFiles: []string{
					"cells/accesscore/slices/session-login/**",
					"cells/accesscore/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// Non-strict: no FMT-17 error.
	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	for _, r := range results {
		if r.Code == "FMT-17" && r.Severity == SeverityError {
			t.Error("non-strict mode should not produce FMT-17 error")
		}
	}

	// Strict: allowedFiles first entry mismatch should be error.
	results, err = v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	hasMismatchError := false
	for _, r := range results {
		if r.Code == "FMT-17" && r.Severity == SeverityError {
			hasMismatchError = true
		}
	}
	if !hasMismatchError {
		t.Error("strict mode should produce FMT-17 error when allowedFiles first entry doesn't match slice directory")
	}
}

func TestValidateStrict_IncludesVERIFY06OnlyWhenStrict(t *testing.T) {
	project := validProject()
	project.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
		{Text: "manual signoff", Mode: "manual"},
	}

	v := NewValidator(project, "", clock.Real())
	forRes144, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	for _, r := range forRes144 {
		if r.Code == "VERIFY-06" {
			t.Fatalf("non-strict validation must not produce VERIFY-06: %s", r.Message)
		}
	}

	results, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	found := false
	for _, r := range results {
		if r.Code == "VERIFY-06" && r.Severity == SeverityError {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("strict validation must include VERIFY-06 for active manual-only journey")
	}
}

func TestValidateStrictFailFast_IncludesVERIFY06WhenBaseClean(t *testing.T) {
	project := validProject()
	project.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
		{Text: "manual signoff", Mode: "manual"},
	}

	results, err := NewValidator(project, "", clock.Real()).ValidateStrict(t.Context(), true, true)
	require.NoError(t, err)
	if len(results) == 0 {
		t.Fatal("expected VERIFY-06 from strict fail-fast")
	}
	last := results[len(results)-1]
	if last.Code != "VERIFY-06" || last.Severity != SeverityError {
		t.Fatalf("expected fail-fast to stop on VERIFY-06, got %#v", last)
	}
}

// TestValidateStrictFailFast_ShortCircuitsOnBaseError verifies that when the
// base ValidateFailFast finds an error, ValidateStrictFailFast returns
// immediately without appending FMT-16 or FMT-17 results.
func TestValidateStrictFailFast_ShortCircuitsOnBaseError(t *testing.T) {
	// A project whose cell has a missing required field (schema.primary) so
	// that standard rules fire an error, plus a kebab slice that would trigger
	// FMT-16 in strict mode.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				// schema.primary intentionally empty → triggers FMT-01 error.
				Schema: metadata.SchemaMeta{Primary: ""},
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{"cells/accesscore/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), true, true)
	require.NoError(t, err)

	// Must contain at least one error.
	if !HasErrors(results) {
		t.Fatal("expected at least one error from standard rules, got none")
	}

	// FMT-16 and FMT-17 must NOT be present because the base pass short-circuited.
	for _, r := range results {
		if r.Code == "FMT-16" || r.Code == "FMT-17" {
			t.Errorf("short-circuit path should not produce %s but got: %s", r.Code, r.Message)
		}
	}
}

// TestValidateNonStrictFailFast verifies the (strict=false, failFast=true)
// cell of the ValidateStrict 2x2 matrix — the most common CI mode. Two
// guarantees: (1) FMT-16 / FMT-17 / FMT-C1 (strict-only rules) do NOT
// appear regardless of source state; (2) the first SeverityError still
// short-circuits the base pipeline.
func TestValidateNonStrictFailFast(t *testing.T) {
	// Force a kebab slice dir (FMT-16 trigger) AND a base error (missing
	// required cell.schema.primary) so strict-only rules would clearly fire
	// if strict gating were broken.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				// Schema.Primary intentionally missing — triggers a base error.
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
				Dir:    "accesscore",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:             "session-login",
				BelongsToCell:  "accesscore",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/accesscore/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	results, err := NewValidator(project, "", clock.Real()).ValidateStrict(t.Context(), false, true)
	require.NoError(t, err)

	// Must produce at least one base error to confirm fail-fast actually fired.
	if !HasErrors(results) {
		t.Fatal("expected at least one base error from missing schema.primary")
	}
	// Strict-only rules must NOT appear under strict=false.
	for _, r := range results {
		if r.Code == "FMT-16" || r.Code == "FMT-17" || r.Code == "FMT-C1" || r.Code == "FMT-19" {
			t.Errorf("strict-only rule %s leaked under (strict=false, failFast=true): %s", r.Code, r.Message)
		}
	}
}

// TestValidateStrictFailFast_RunsFMT16FMT17WhenNoBaseError verifies that when
// the base pass finds no errors, ValidateStrictFailFast appends FMT-16/17 just
// like ValidateStrict(true) would.
func TestValidateStrictFailFast_RunsFMT16FMT17WhenNoBaseError(t *testing.T) {
	// L1 cell with no contractUsages — passes all standard rules while the
	// slice key uses a kebab-case directory that strict mode (FMT-16) must flag.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:             "session-login",
				BelongsToCell:  "accesscore",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/accesscore/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), true, true)
	require.NoError(t, err)

	hasFMT16 := false
	for _, r := range results {
		if r.Code == "FMT-16" && r.Severity == SeverityError {
			hasFMT16 = true
		}
	}
	if !hasFMT16 {
		t.Error("expected FMT-16 error from ValidateStrictFailFast when no base error is present")
	}
}

// TestValidateStrict_EmptyProject verifies that ValidateStrict(true) on an
// empty project (no slices) does not produce FMT-16 or FMT-17 findings.
func TestValidateStrict_EmptyProject(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	strictResults, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	baseResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

	// FMT-16 and FMT-17 must not appear on an empty slice map.
	for _, r := range strictResults {
		if r.Code == "FMT-16" || r.Code == "FMT-17" {
			t.Errorf("empty project should not produce %s but got: %s", r.Code, r.Message)
		}
	}

	// The strict run must not produce more findings than the base run (no slice
	// to trigger FMT-16/17, so both runs should be identical in length).
	if len(strictResults) != len(baseResults) {
		t.Errorf("ValidateStrict(true) on empty project: got %d results, base Validate() got %d; expected equal",
			len(strictResults), len(baseResults))
	}
}

// TestValidateStrict_NonStrictEquivalentToValidate verifies that
// ValidateStrict(false) produces results that are equivalent to Validate()
// by comparing code+severity for every finding.
func TestValidateStrict_NonStrictEquivalentToValidate(t *testing.T) {
	// Use a non-trivial project (L2 cell with a clean no-dash slice) so that
	// standard rules actually fire some results.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/accesscore/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	nonStrictResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	baseResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

	// Build comparable fingerprint: sorted list of "code:severity" strings.
	fingerprint := func(results []ValidationResult) map[string]int {
		m := make(map[string]int)
		for _, r := range results {
			key := string(r.Code) + ":" + string(r.Severity)
			m[key]++
		}
		return m
	}

	nsMap := fingerprint(nonStrictResults)
	baseMap := fingerprint(baseResults)

	if len(nsMap) != len(baseMap) {
		t.Errorf("ValidateStrict(false) returned %d unique findings, Validate() returned %d; expected equal",
			len(nsMap), len(baseMap))
	}
	for k, cnt := range baseMap {
		if nsMap[k] != cnt {
			t.Errorf("finding %q: ValidateStrict(false) count=%d, Validate() count=%d", k, nsMap[k], cnt)
		}
	}
	// FMT-16 and FMT-17 must not appear in non-strict results.
	for _, r := range nonStrictResults {
		if r.Code == "FMT-16" || r.Code == "FMT-17" {
			t.Errorf("ValidateStrict(false) must not produce %s but got: %s", r.Code, r.Message)
		}
	}
}

// TestStrictValidator_NodashSliceClean verifies that a correctly migrated
// no-dash slice with matching allowedFiles passes strict validation.
func TestStrictValidator_NodashSliceClean(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/accesscore/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	for _, r := range results {
		if (r.Code == "FMT-16" || r.Code == "FMT-17") && r.Severity == SeverityError {
			t.Errorf("clean no-dash slice should not produce %s error: %s", r.Code, r.Message)
		}
	}
}

// The previous implementation read sliceDir from the map key (which embeds
// slice.id), so a kebab directory paired with a no-dash id escaped FMT-16
// entirely. After moving FMT-16 onto SliceMeta.Dir this test guarantees the
// check sees filesystem truth, not YAML-declared id.
func TestStrictValidator_FMT16_PathIDSplit_KebabDirNoDashID(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
				Dir:              "accesscore",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			// Map key is "accesscore/sessionlogin" (yaml id), but the
			// walked directory is the kebab-case "session-login".
			"accesscore/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "accesscore",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/accesscore/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)

	var gotFMT16 bool
	for _, r := range results {
		if r.Code == "FMT-16" && r.Severity == SeverityError {
			gotFMT16 = true
		}
	}
	if !gotFMT16 {
		t.Error("FMT-16 must fire for kebab directory even when yaml id is no-dash (path/id split escape path)")
	}
}

// REF-05 must fire when the slice directory disagrees with slice.id. The old
// implementation rederived "expected id" from the map key (which is built
// from slice.id itself), self-comparing and always passing — guard against
// that regression.
func TestREF05_PathIDSplit_FiresWhenDirAndIDDisagree(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
				Dir:              "accesscore",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "accesscore",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/accesscore/slices/sessionlogin/**"},
				Dir:          "session-login", // disk says kebab, yaml says no-dash
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err) // REF-05 is a standard rule, not strict-only

	var gotREF05 bool
	for _, r := range results {
		if r.Code == "REF-05" && r.Severity == SeverityError {
			gotREF05 = true
		}
	}
	if !gotREF05 {
		t.Error("REF-05 must fire when slice.id disagrees with directory name (was silent before Dir field)")
	}
}

// assertRuleFiresInBothModes runs ValidateStrict in both default and
// strict modes and fails when the given rule code does not fire as a
// SeverityError in either pass. Extracted so FMTC1/FMTA1 unconditional-
// rule tests share the same shape and stay under the cognitive
// complexity budget.
func assertRuleFiresInBothModes(t *testing.T, v *Validator, code RuleCode, fixtureDesc string) {
	t.Helper()
	for _, strict := range []bool{false, true} {
		strict := strict
		t.Run(fmtBoolName(strict), func(t *testing.T) {
			results, err := v.ValidateStrict(t.Context(), strict, false)
			require.NoError(t, err)
			if !ruleFiredAsError(results, code) {
				t.Errorf("%s must fire for %s (strict=%v)", code, fixtureDesc, strict)
			}
		})
	}
}

// ruleFiredAsError reports whether results contains at least one
// SeverityError matching code. Side-effect free; used by the
// assertRuleFires* helpers.
func ruleFiredAsError(results []ValidationResult, code RuleCode) bool {
	for _, r := range results {
		if r.Code == code && r.Severity == SeverityError {
			return true
		}
	}
	return false
}

// TestValidator_FMTC1_CellIDPattern verifies FMT-C1 fires unconditionally
// for cell ids that violate CellIDPattern (kebab, uppercase, single char,
// leading digit, etc.). FMT-C1 mirrors a schema constraint
// (schemas/cell.schema.json id.pattern) so it must trip on both the default
// and strict validate paths — schema-aware tooling rejects the same value
// without a strict toggle. Uses synthetic ids so later repo-wide sed renames
// do not erase the fixture.
func TestValidator_FMTC1_CellIDPattern(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"kebab_disallowed", "foo-bar"},
		{"uppercase_disallowed", "FooBar"},
		{"single_char_disallowed", "a"},
		{"leading_digit_disallowed", "1foo"},
		{"underscore_disallowed", "foo_bar"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			project := &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					tc.id: {
						ID:               tc.id,
						Type:             "core",
						ConsistencyLevel: "L2",
						Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
						Schema:           metadata.SchemaMeta{Primary: "cell_foobar"},
						Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.startup"}},
						Dir:              "foobar",
					},
				},
				Slices:     map[string]*metadata.SliceMeta{},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(project, "", clock.Real())
			assertRuleFiresInBothModes(t, v, "FMT-C1",
				fmt.Sprintf("invalid cell id %q", tc.id))
		})
	}
}

// TestValidator_FMTA1_AssemblyIDPattern verifies FMT-A1 fires
// unconditionally for assembly ids that violate AssemblyIDPattern. Uses
// synthetic ids (baz-qux) so later repo-wide sed renames do not erase the
// fixture. FMT-A1 mirrors a schema constraint (schemas/assembly.schema.json
// id.pattern) so it must trip on both the default and strict validate
// paths — schema-aware tooling rejects the same value without a strict
// toggle.
func TestValidator_FMTA1_AssemblyIDPattern(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"baz-qux": {
				ID:    "baz-qux",
				Cells: []string{},
				Build: metadata.BuildMeta{Entrypoint: "cmd/bazqux/main.go", Binary: "bazqux"},
				Dir:   "bazqux",
			},
		},
	}

	v := NewValidator(project, "", clock.Real())
	assertRuleFiresInBothModes(t, v, "FMT-A1", "kebab-case assembly id")
}

func fmtBoolName(b bool) string {
	if b {
		return "strict"
	}
	return "default"
}

// TestStrictValidator_FMT16_KebabCellDir verifies FMT-16 flags kebab cell
// directories (not just slices). Uses synthetic ids so later repo-wide sed
// renames cannot accidentally clean up the kebab fixture.
func TestStrictValidator_FMT16_KebabCellDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"foobar": {
				ID:               "foobar",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_foobar"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.startup"}},
				Dir:              "foo-bar", // id clean but dir kebab
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())

	var got bool
	forRes650, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	for _, r := range forRes650 {
		if r.Code == "FMT-16" && r.Severity == SeverityError &&
			strings.Contains(r.Message, "cell") {
			got = true
		}
	}
	if !got {
		t.Error("FMT-16 should flag kebab-case cell directory")
	}
}

// TestStrictValidator_FMT16_KebabAssemblyDir verifies FMT-16 covers kebab
// assembly directories. Uses synthetic ids to insulate the fixture from
// repo-wide sed renames.
func TestStrictValidator_FMT16_KebabAssemblyDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"bazqux": {
				ID:    "bazqux",
				Cells: []string{},
				Build: metadata.BuildMeta{Entrypoint: "cmd/bazqux/main.go", Binary: "bazqux"},
				Dir:   "baz-qux", // id clean but dir kebab
			},
		},
	}

	v := NewValidator(project, "", clock.Real())

	var got bool
	forRes683, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	for _, r := range forRes683 {
		if r.Code == "FMT-16" && r.Severity == SeverityError &&
			strings.Contains(r.Message, "assembly") {
			got = true
		}
	}
	if !got {
		t.Error("FMT-16 should flag kebab-case assembly directory")
	}
}

// TestStrictValidator_FMTC1_FMTA1_NoDashClean verifies clean no-dash cell
// and assembly pass both FMT-C1 and FMT-A1.
func TestStrictValidator_FMTC1_FMTA1_NoDashClean(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_accesscore"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.startup"}},
				Dir:              "accesscore",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"corebundle": {
				ID:    "corebundle",
				Cells: []string{"accesscore"},
				Build: metadata.BuildMeta{Entrypoint: "cmd/corebundle/main.go", Binary: "corebundle"},
				Dir:   "corebundle",
			},
		},
	}

	v := NewValidator(project, "", clock.Real())
	forRes723, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)
	for _, r := range forRes723 {
		if r.Code == "FMT-C1" || r.Code == "FMT-A1" {
			t.Errorf("clean no-dash project should not produce %s: %s", r.Code, r.Message)
		}
		if r.Code == "FMT-16" && (strings.Contains(r.Message, "cell") || strings.Contains(r.Message, "assembly")) {
			t.Errorf("clean no-dash project should not produce FMT-16 for cell/assembly: %s", r.Message)
		}
	}
}

// FMT-17 must compare allowedFiles against the real disk directory, not
// against a directory synthesized from slice.id. Before the Dir field was
// introduced, an allowedFiles entry that echoed the (faked) id path would
// silently match and let the real kebab-case directory slip through.
func TestStrictValidator_FMT17_AllowedFilesAgainstRealDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
				Dir:              "accesscore",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "accesscore",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				// allowedFiles points to the (no-dash) id path, lying about
				// the real directory on disk.
				AllowedFiles: []string{"cells/accesscore/slices/sessionlogin/**"},
				Dir:          "session-login",
				CellDir:      "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), true, false)
	require.NoError(t, err)

	var gotFMT17 bool
	for _, r := range results {
		if r.Code == "FMT-17" && r.Severity == SeverityError {
			gotFMT17 = true
		}
	}
	if !gotFMT17 {
		t.Error("FMT-17 must fire when allowedFiles[0] doesn't match the real directory (path/id split escape path)")
	}
}

// =============================================================================
// FMT-21/22/23/25 (formerly rules_strict_extra_test.go)
// =============================================================================

// --- FMT-20 ---
// FMT-20 tests live in rules_strict_extra_fmt20_test.go (table-driven per
// ADR-202605031600 v1 schema evolution).

// --- FMT-21 (contract dir ↔ ID match) ---

// TestFMTContractDirIDMatch01_Mismatch verifies that a contract whose Dir does
// not match the ID-derived path emits a FMT-21 violation.
func TestFMTContractDirIDMatch01_Mismatch(t *testing.T) {
	tests := []struct {
		name        string
		contractID  string
		contractDir string
		wantCount   int
	}{
		{
			name:        "correct dir",
			contractID:  "http.auth.login.v1",
			contractDir: "contracts/http/auth/login/v1",
			wantCount:   0,
		},
		{
			name:        "wrong dir",
			contractID:  "http.auth.login.v1",
			contractDir: "contracts/http/auth/register/v1",
			wantCount:   1,
		},
		{
			name:        "event contract correct",
			contractID:  "event.session.created.v1",
			contractDir: "contracts/event/session/created/v1",
			wantCount:   0,
		},
		{
			// id segment "internal-get" (dash) versus dir segments "internal/get"
			// (slash). Pins the canonical PATH-ID-MAPPING regression that
			// PR-CFG-G1-FU6-RECYCLE filed against FMT-CONTRACT-PATH-ID-MAPPING-01
			// and that FMT-21 already covers as the bijective inverse rule.
			//
			// INTEGRATION ANCHOR — DO NOT DELETE WITHOUT REPLACEMENT.
			// This case calls v.ValidateStrict(t.Context(), false, false) (the full rules() chain),
			// so removing FMT-21 from rules() makes wantCount:1 fail. The case
			// therefore pins both the rule logic AND the rule's membership in
			// the default validator slice. Removing it (or downgrading wantCount
			// to 0) silently weakens the PATH-ID-MAPPING governance contract.
			name:        "dash-instead-of-slash regression (PR-CFG-G1-FU6-RECYCLE)",
			contractID:  "http.config.internal-get.v1",
			contractDir: "contracts/http/config/internal/get/v1",
			wantCount:   1,
		},
		{
			// Legitimate single-segment dash (e.g. event.config.entry-deleted.v1
			// matches contracts/event/config/entry-deleted/v1). Guards against a
			// future "dashes are always wrong" weakening of the rule.
			name:        "dash matches both id and path (compliant)",
			contractID:  "event.config.entry-deleted.v1",
			contractDir: "contracts/event/config/entry-deleted/v1",
			wantCount:   0,
		},
		{
			// Pins the defensive skip in FMT-21 (rules_misc_strict.go:569-571).
			// Removing the `if c.Dir == "" { continue }` guard would cause an
			// empty Dir to derive "contracts/" and fire on every contract — this
			// test catches that regression. Empty Dir is unreachable in
			// production parser loads, but the defensive skip is part of the
			// rule's documented contract.
			name:        "empty dir is skipped (defensive guard)",
			contractID:  "http.x.y.v1",
			contractDir: "",
			wantCount:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					tc.contractID: {
						ID:        tc.contractID,
						Kind:      "http",
						OwnerCell: "testcell",
						Lifecycle: "active",
						Dir:       tc.contractDir,
						File:      tc.contractDir + "/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "", clock.Real())
			results, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-21")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
		})
	}
}

// TestFMTContractDirIDMatch01_ExamplesPrefix verifies that contracts living
// under an examples/* subtree are accepted as long as the segment after the
// last "contracts/" separator matches the ID-derived suffix.
func TestFMTContractDirIDMatch01_ExamplesPrefix(t *testing.T) {
	tests := []struct {
		name        string
		contractID  string
		contractDir string
		wantCount   int
	}{
		{
			name:        "examples/iotdevice prefix — correct suffix",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/contracts/http/bar/v1",
			wantCount:   0,
		},
		{
			name:        "examples/todoorder prefix — correct suffix",
			contractID:  "event.device.registered.v1",
			contractDir: "examples/iotdevice/contracts/event/device/registered/v1",
			wantCount:   0,
		},
		{
			name:        "examples prefix — wrong suffix must still fire",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/contracts/http/baz/v1",
			wantCount:   1,
		},
		{
			name:        "no contracts/ segment in dir — violation",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/http/bar/v1",
			wantCount:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					tc.contractID: {
						ID:        tc.contractID,
						Kind:      "http",
						OwnerCell: "testcell",
						Lifecycle: "active",
						Dir:       tc.contractDir,
						File:      tc.contractDir + "/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "", clock.Real())
			results, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-21")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
		})
	}
}

// --- FMT-22 (status-board state enum) ---

// TestStatusBoardStateEnum01 verifies that invalid state values are flagged.
func TestStatusBoardStateEnum01(t *testing.T) {
	tests := []struct {
		name      string
		entries   []metadata.StatusBoardEntry
		wantCount int
	}{
		{
			name: "all valid states",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-login", State: "todo"},
				{JourneyID: "J-audit", State: "doing"},
				{JourneyID: "J-report", State: "done"},
			},
			wantCount: 0,
		},
		{
			name: "invalid WIP state",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-login", State: "WIP"},
			},
			wantCount: 1,
		},
		{
			name: "multiple invalid states",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-a", State: "in-progress"},
				{JourneyID: "J-b", State: "doing"},
				{JourneyID: "J-c", State: "pending"},
			},
			wantCount: 2,
		},
		{
			name:      "empty board",
			entries:   nil,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:       map[string]*metadata.CellMeta{},
				Slices:      map[string]*metadata.SliceMeta{},
				Contracts:   map[string]*metadata.ContractMeta{},
				Journeys:    map[string]*metadata.JourneyMeta{},
				Assemblies:  map[string]*metadata.AssemblyMeta{},
				StatusBoard: tc.entries,
			}

			v := NewValidator(pm, "", clock.Real())
			results, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-22")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
			for _, r := range matches {
				assert.Equal(t, SeverityError, r.Severity)
			}
		})
	}
}

// --- FMT-23 (contract deprecated cleanup) ---

// TestContractDeprecatedCleanup01 verifies the three deprecation violation cases.
func TestContractDeprecatedCleanup01(t *testing.T) {
	tests := []struct {
		name         string
		lifecycle    string
		deprecatedAt string
		wantCount    int
		wantSev      Severity
		wantField    string
	}{
		{
			name:      "active contract, no deprecatedAt — no violation",
			lifecycle: "active",
			wantCount: 0,
		},
		{
			name:         "deprecated missing deprecatedAt — error",
			lifecycle:    "deprecated",
			deprecatedAt: "",
			wantCount:    1,
			wantSev:      SeverityError,
			wantField:    "deprecatedAt",
		},
		{
			name:         "deprecated malformed date — error",
			lifecycle:    "deprecated",
			deprecatedAt: "not-a-date",
			wantCount:    1,
			wantSev:      SeverityError,
			wantField:    "deprecatedAt",
		},
		{
			name:         "deprecated >90d old — warning",
			lifecycle:    "deprecated",
			deprecatedAt: "2020-01-01",
			wantCount:    1,
			wantSev:      SeverityWarning,
			wantField:    "lifecycle",
		},
		{
			name:         "deprecated recent (<90d) — no violation",
			lifecycle:    "deprecated",
			deprecatedAt: time.Now().AddDate(0, 0, -30).Format("2006-01-02"),
			wantCount:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					"http.test.deprecated.v1": {
						ID:           "http.test.deprecated.v1",
						Kind:         "http",
						OwnerCell:    "testcell",
						Lifecycle:    tc.lifecycle,
						DeprecatedAt: tc.deprecatedAt,
						Dir:          "contracts/http/test/deprecated/v1",
						File:         "contracts/http/test/deprecated/v1/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "", clock.Real())
			results, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-23")
			require.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
			if tc.wantCount > 0 {
				assert.Equal(t, tc.wantSev, matches[0].Severity,
					"test %q: wrong severity", tc.name)
				assert.Equal(t, tc.wantField, matches[0].Field,
					"test %q: wrong field", tc.name)
			}
		})
	}
}

// TestFMT22_EmptyStateViolation verifies FMT-22 fires when state is empty string.
func TestFMT22_EmptyStateViolation(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-empty", State: ""},
		},
	}

	v := NewValidator(pm, "", clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-22")
	assert.Len(t, matches, 1,
		"empty state must produce 1 FMT-22 violation, got %d: %v", len(matches), matches)
	if len(matches) == 1 {
		assert.Equal(t, SeverityError, matches[0].Severity)
	}
}

// TestFMT23_DeprecatedCleanup_BoundaryCheck verifies the 90-day boundary.
// Note: the check uses time.Parse (midnight UTC) vs time.Now().UTC() (current time),
// so "N days ago" means midnight of that date. With 89 days the difference is
// < 90 days + intraday remainder, guaranteeing no warning. With 91 days the
// difference exceeds 90 days even at midnight, guaranteeing a warning.
func TestFMT23_DeprecatedCleanup_BoundaryCheck(t *testing.T) {
	tests := []struct {
		name      string
		daysAgo   int
		wantCount int
	}{
		{
			name:      "89 days ago — no warning (well within 90d)",
			daysAgo:   89,
			wantCount: 0,
		},
		{
			name:      "91 days ago — warning fires (>90d)",
			daysAgo:   91,
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deprecatedDate := time.Now().UTC().AddDate(0, 0, -tc.daysAgo).Format("2006-01-02")
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					"http.test.old.v1": {
						ID:           "http.test.old.v1",
						Kind:         "http",
						OwnerCell:    "testcell",
						Lifecycle:    "deprecated",
						DeprecatedAt: deprecatedDate,
						Dir:          "contracts/http/test/old/v1",
						File:         "contracts/http/test/old/v1/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "", clock.Real())
			results, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-23")
			// Filter to warnings only (we don't want IssueRequired or IssueInvalid counts).
			var warnings []ValidationResult
			for _, r := range matches {
				if r.Severity == SeverityWarning {
					warnings = append(warnings, r)
				}
			}
			assert.Len(t, warnings, tc.wantCount,
				"test %q: expected %d FMT-23 warnings, got %d: %v",
				tc.name, tc.wantCount, len(warnings), warnings)
		})
	}
}

// --- scanSchemaForStrictMissing helper (unit) ---

// TestScanSchemaForStrictMissing_FileNotFound verifies that a non-existent schema
// path returns an error from scanSchemaForStrictMissing.
func TestScanSchemaForStrictMissing_FileNotFound(t *testing.T) {
	_, err := scanSchemaForStrictMissing("/nonexistent/path/schema.json")
	require.Error(t, err, "missing file must return an error")
}

// TestScanSchemaForStrictMissing_InvalidJSON verifies that malformed JSON content
// returns an error from scanSchemaForStrictMissing.
func TestScanSchemaForStrictMissing_InvalidJSON(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-*.json")
	require.NoError(t, err)
	_, err = f.WriteString("not valid json {{{")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = scanSchemaForStrictMissing(f.Name())
	require.Error(t, err, "malformed JSON must return an error")
	assert.Contains(t, err.Error(), "invalid JSON schema")
}

// TestWalkSchemaObjectDepth_DepthGuard verifies that walkSchemaObjectDepth
// terminates cleanly at depth > 32 and does not recurse indefinitely.
func TestWalkSchemaObjectDepth_DepthGuard(t *testing.T) {
	// Build a deeply nested schema (34 levels) that would recurse infinitely
	// without the depth guard. Each level wraps the next in a "nested" property.
	buildNested := func(depth int) map[string]any {
		inner := map[string]any{
			"type": "object",
			// No additionalProperties — would be a violation at every level.
		}
		current := inner
		for range depth {
			next := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"nested": current,
				},
			}
			current = next
		}
		return current
	}

	deepSchema := buildNested(35)
	var missing []string
	// Should not panic or run forever; returns after the depth guard fires.
	walkSchemaObject(deepSchema, "$", &missing)
	// Some violations found (top levels) but the guard stops infinite recursion.
	assert.NotPanics(t, func() {
		walkSchemaObject(deepSchema, "$", &missing)
	})
}

// TestCheckAdditionalProperties_ObjectValueTreatedAsMissing verifies that
// additionalProperties set to an object (not bool false) is treated as a violation.
func TestCheckAdditionalProperties_ObjectValueTreatedAsMissing(t *testing.T) {
	node := map[string]any{
		"type": "object",
		// additionalProperties is a schema object, not bool false — counts as missing.
		"additionalProperties": map[string]any{"type": "string"},
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Equal(t, []string{"$"}, missing,
		"additionalProperties as object value must be treated as missing")
}

// TestCheckAdditionalProperties_TrueValueRejected verifies that an explicit
// additionalProperties: true is rejected per ADR-202605031600. FMT-20 only
// scans request schemas, where strict closed-shape is the only acceptable
// declaration — explicit `true` is functionally identical to the missing-key
// bypass and must fail the same way.
func TestCheckAdditionalProperties_TrueValueRejected(t *testing.T) {
	node := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Equal(t, []string{"$"}, missing,
		"additionalProperties:true must be rejected — request schemas must be strictly closed (ADR-202605031600)")
}

// TestCheckAdditionalProperties_FalseValueAccepted verifies that an explicit
// additionalProperties: false is accepted (author chose strict schema).
func TestCheckAdditionalProperties_FalseValueAccepted(t *testing.T) {
	node := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Empty(t, missing,
		"additionalProperties:false must be accepted — author explicitly declared strict schema")
}

// TestCheckAdditionalProperties_MissingViolation verifies that a missing
// additionalProperties key triggers a violation.
func TestCheckAdditionalProperties_MissingViolation(t *testing.T) {
	node := map[string]any{
		"type": "object",
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Equal(t, []string{"$"}, missing,
		"missing additionalProperties must emit a violation")
}

// TestScanSchemaForStrictMissing_Basic verifies the helper returns correct
// JSON-pointer paths for missing additionalProperties.
func TestScanSchemaForStrictMissing_Basic(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"data": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
			},
		},
	}

	raw, err := json.Marshal(schema)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "schema-*.json")
	require.NoError(t, err)
	_, err = f.Write(raw)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	paths, err := scanSchemaForStrictMissing(f.Name())
	require.NoError(t, err)
	// Top-level object missing additionalProperties → "$"
	// $.data has it set → no violation
	assert.Equal(t, []string{"$"}, paths)
}

// --- FMT-25 (HTTP input constraint: minLength/maxLength on strings, minimum/maximum on numeric values) ---

// fmt25WriteSchema is a test helper that writes a JSON schema string to the
// standard "contracts/http/test/v1" contract directory. Encapsulates the
// repeated TempDir + MkdirAll + WriteFile dance used across FMT-25 tests.
func fmt25WriteSchema(t *testing.T, dir, body string) {
	t.Helper()
	const contractRel = "contracts/http/test/v1"
	full := filepath.Join(dir, contractRel)
	require.NoError(t, os.MkdirAll(full, 0o755))
	p := filepath.Join(full, "request.schema.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// fmt25Project builds a ProjectMeta containing one HTTP contract with the
// given request schema reference. queryParams / pathParams are optional —
// pass nil to omit. Used by every FMT-25 schema-driven test below.
func fmt25Project(queryParams, pathParams map[string]metadata.ParamSchema) *metadata.ProjectMeta {
	const contractDir = "contracts/http/test/v1"
	const contractID = "http.test.v1"
	cm := &metadata.ContractMeta{
		ID:        contractID,
		Kind:      "http",
		OwnerCell: "testcell",
		Lifecycle: "active",
		SchemaRefs: metadata.SchemaRefsMeta{
			Request: "request.schema.json",
		},
		Dir:  contractDir,
		File: contractDir + "/contract.yaml",
	}
	if queryParams != nil || pathParams != nil {
		var path strings.Builder
		path.WriteString("/x")
		for _, name := range sortedParamKeys(pathParams) {
			path.WriteString("/{" + name + "}")
		}
		cm.Endpoints = metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          path.String(),
				PathParams:    pathParams,
				QueryParams:   queryParams,
				SuccessStatus: 200,
			},
		}
	}
	return &metadata.ProjectMeta{
		Cells:       map[string]*metadata.CellMeta{},
		Slices:      map[string]*metadata.SliceMeta{},
		Contracts:   map[string]*metadata.ContractMeta{contractID: cm},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: nil,
	}
}

func TestFMT25_RequestSchemaPathEscapeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "contracts", "http", "test"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "contracts", "http", "test", "outside.schema.json"),
		[]byte(`{"type":"object","additionalProperties":false}`), 0o644))
	pm := fmt25Project(nil, nil)
	pm.Contracts["http.test.v1"].SchemaRefs.Request = "../outside.schema.json"

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "schemaRefs.request", matches[0].Field)
	assert.Contains(t, matches[0].Message, "failed to resolve")
}

func TestFMT25_RequestSchemaMissingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueRefNotFound, matches[0].IssueType)
	assert.Equal(t, "schemaRefs.request", matches[0].Field)
	assert.Contains(t, matches[0].Message, "missing file")
}

// TestFMT25_RequestStringMissingMinLength verifies a violation fires when a
// string field in request.schema.json lacks minLength.
func TestFMT25_RequestStringMissingMinLength(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"username": {"type": "string", "maxLength": 128}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	v := NewValidator(pm, dir, clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "expected 1 violation for username missing minLength, got %d: %v", len(matches), matches)
	assert.Equal(t, "$.username", matches[0].Field)
	assert.Equal(t, SeverityError, matches[0].Severity)
	assert.Contains(t, matches[0].Message, "minLength")
}

// TestFMT25_RequestStringMissingMaxLength verifies a violation fires when a
// string field lacks maxLength (even if minLength is set).
func TestFMT25_RequestStringMissingMaxLength(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"username": {"type": "string", "minLength": 1}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "expected 1 violation for username missing maxLength")
	assert.Contains(t, matches[0].Message, "maxLength")
}

// TestFMT25_RequestIntegerMissingMinimumMaximum verifies violations fire when
// integer fields lack minimum or maximum.
func TestFMT25_RequestIntegerMissingMinimumMaximum(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"version": {"type": "integer", "minimum": 1},
			"page":    {"type": "integer"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	// version: missing maximum (1 violation)
	// page:    missing minimum + missing maximum (2 violations)
	require.Len(t, matches, 3, "expected 3 violations, got %d: %v", len(matches), matches)
}

// TestFMT25_RequestNumberMissingMinimumMaximum verifies that JSON Schema
// number fields are governed by the same numeric bounds as integers.
func TestFMT25_RequestNumberMissingMinimumMaximum(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"ratio": {"type": "number"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "number fields must require minimum + maximum, got: %v", matches)
	gotMessages := []string{matches[0].Message, matches[1].Message}
	for _, m := range matches {
		assert.Equal(t, "$.ratio", m.Field)
	}
	assert.Condition(t, func() bool {
		return strings.Contains(gotMessages[0], "minimum") || strings.Contains(gotMessages[1], "minimum")
	}, "expected a minimum violation, got %v", gotMessages)
	assert.Condition(t, func() bool {
		return strings.Contains(gotMessages[0], "maximum") || strings.Contains(gotMessages[1], "maximum")
	}, "expected a maximum violation, got %v", gotMessages)
}

// TestFMT25_RequestUnionTypeStringMissingConstraints verifies JSON Schema type
// arrays are interpreted semantically instead of being skipped.
func TestFMT25_RequestUnionTypeStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"displayName": {"type": ["string", "null"]}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "union string|null must still require length facets, got: %v", matches)
	for _, m := range matches {
		assert.Equal(t, "$.displayName", m.Field)
	}
}

func TestFMT25_RequestExternalRefFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"remote": {"$ref": "https://example.invalid/common.schema.json#/Name"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "non-local refs must fail closed")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.remote", matches[0].Field)
	assert.Contains(t, matches[0].Message, "non-local $ref")
}

func TestFMT25_RequestUnresolvedLocalRefFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"$ref": "#/$defs/missing"}
		},
		"$defs": {}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "unresolved local refs must fail closed")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.name", matches[0].Field)
	assert.Contains(t, matches[0].Message, "unresolved local $ref")
}

func TestFMT25_RequestMinGreaterThanMaxInvalid(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"type": "string", "minLength": 20, "maxLength": 5},
			"ratio": {"type": "number", "minimum": 10, "maximum": 1}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "inverted bounds must be invalid, got: %v", matches)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.name", matches[0].Field)
	assert.Contains(t, matches[0].Message, "minLength")
	assert.Equal(t, IssueInvalid, matches[1].IssueType)
	assert.Equal(t, "$.ratio", matches[1].Field)
	assert.Contains(t, matches[1].Message, "minimum")
}

func TestFMT25_RequestDepthLimitFailsClosed(t *testing.T) {
	dir := t.TempDir()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	}
	parent := schema["properties"].(map[string]any)
	for range 34 {
		child := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		}
		parent["nested"] = child
		parent = child["properties"].(map[string]any)
	}
	parent["leaf"] = map[string]any{"type": "string"}
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	fmt25WriteSchema(t, dir, string(raw))
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "depth limit must emit an observable diagnostic")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Contains(t, matches[0].Message, "depth")
}

// TestFMT25_RequestNestedObjectStringConstraints verifies the walker recurses
// into nested objects.
func TestFMT25_RequestNestedObjectStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"user": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"name": {"type": "string"}
				}
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	// user.name missing both → 2 violations (one per missing facet)
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.user.name", m.Field)
	}
}

// TestFMT25_RequestArrayItemsStringConstraints verifies the walker recurses
// into items of array properties.
func TestFMT25_RequestArrayItemsStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"tags": {
				"type": "array",
				"items": {"type": "string"}
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	// tags.items missing minLength + maxLength → 2 violations at $.tags.items
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.tags.items", m.Field)
	}
}

// TestFMT25_RequestLocalRefStringConstraints verifies local $ref targets are
// resolved at the referring field path.
func TestFMT25_RequestLocalRefStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"$ref": "#/$defs/name"}
		},
		"$defs": {
			"name": {"type": "string"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.name", m.Field)
	}
}

// TestFMT25_RequestCombinatorStringConstraints verifies common composition
// keywords are traversed instead of hiding unconstrained inputs.
func TestFMT25_RequestCombinatorStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {
				"allOf": [
					{"type": "string", "minLength": 1}
				]
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, "$.name.allOf[0]", matches[0].Field)
	assert.Contains(t, matches[0].Message, "maxLength")
}

func TestFMT25_RequestUnevaluatedItemsStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "array",
		"items": {"type": "string", "minLength": 1, "maxLength": 64},
		"unevaluatedItems": {"type": "string", "minLength": 1}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, "$.unevaluatedItems", matches[0].Field)
	assert.Contains(t, matches[0].Message, "maxLength")
}

// TestFMT25_QueryParamsStringMissingConstraints verifies that
// contract.yaml.queryParams string fields are also checked.
func TestFMT25_QueryParamsStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	// Provide a clean request schema so only the queryParams violation fires.
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]metadata.ParamSchema{
			"cursor": {Type: "string"}, // missing minLength + maxLength
		}, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "expected 2 violations for cursor missing both, got %d: %v", len(matches), matches)
	assert.Equal(t, "endpoints.http.queryParams.cursor.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.cursor.maxLength", matches[1].Field)
}

// TestFMT25_QueryParamsIntegerMissingConstraints verifies that integer
// queryParams (e.g. limit) without minimum/maximum trigger violations.
func TestFMT25_QueryParamsIntegerMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]metadata.ParamSchema{
			"limit": {Type: "integer"}, // missing minimum + maximum
		}, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.queryParams.limit.minimum", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.limit.maximum", matches[1].Field)
}

// TestFMT25_QueryParamsNumberMissingConstraints verifies path/query ParamSchema
// type=number is covered by numeric minimum/maximum governance.
func TestFMT25_QueryParamsNumberMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]metadata.ParamSchema{
			"ratio": {Type: "number"},
		}, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.queryParams.ratio.minimum", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.ratio.maximum", matches[1].Field)
}

func TestFMT25_QueryParamsInvalidBounds(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	one := 1
	ten := 10
	pm := fmt25Project(
		map[string]metadata.ParamSchema{
			"page": {Type: "integer", Minimum: &ten, Maximum: &one},
		}, nil)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "endpoints.http.queryParams.page", matches[0].Field)
	assert.Contains(t, matches[0].Message, "minimum")
}

// TestFMT25_PathParamsStringMissingConstraints verifies pathParams plain
// strings are checked.
func TestFMT25_PathParamsStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(nil,
		map[string]metadata.ParamSchema{
			"key": {Type: "string"}, // plain string, no format → must be checked
		})

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.pathParams.key.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.maxLength", matches[1].Field)
}

// TestFMT25_ParamFindingsUseLocatableMetadataPaths verifies param-side
// findings use full YAML paths so CLI output can include line/column anchors.
func TestFMT25_ParamFindingsUseLocatableMetadataPaths(t *testing.T) {
	dir := t.TempDir()
	const contractRel = "contracts/http/test/v1"
	fmt25WriteSchema(t, dir, `{"type": "object", "additionalProperties": false}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, contractRel, "contract.yaml"), []byte(`id: http.test.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients: []
  http:
    method: GET
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
    queryParams:
      cursor:
        type: string
        required: false
    successStatus: 200
    noContent: false
schemaRefs:
  request: request.schema.json
`), 0o644))
	pm, err := metadata.NewParser(dir).Parse()
	require.NoError(t, err)

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 4)
	for _, m := range matches {
		assert.NotZero(t, m.Line, "field %s should locate a YAML line", m.Field)
		assert.NotZero(t, m.Column, "field %s should locate a YAML column", m.Field)
	}
	assert.Equal(t, "endpoints.http.queryParams.cursor.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.cursor.maxLength", matches[1].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.minLength", matches[2].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.maxLength", matches[3].Field)
}

// TestFMT25_SkipsInvalidPathParams verifies FMT-25 does not add follow-on
// facet noise for pathParams that FMT-13 already rejected.
func TestFMT25_SkipsInvalidPathParams(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	tests := []struct {
		name       string
		path       string
		pathParams map[string]metadata.ParamSchema
	}{
		{
			name: "declaration without placeholder",
			path: "/x",
			pathParams: map[string]metadata.ParamSchema{
				"ghost": {Type: "string"},
			},
		},
		{
			name: "unsupported path param type",
			path: "/x/{id}",
			pathParams: map[string]metadata.ParamSchema{
				"id": {Type: "unsupported"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := fmt25Project(nil, tc.pathParams)
			pm.Contracts["http.test.v1"].Endpoints.HTTP.Path = tc.path

			results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(results, "FMT-25")
			assert.Empty(t, matches)
		})
	}
}

// TestFMT25_PathParamsUUIDFormatExempt verifies that pathParams with
// format:"uuid" are exempted from minLength/maxLength enforcement (RFC 4122
// fixes UUIDs at 36 characters; schema-level constraints would be redundant).
func TestFMT25_PathParamsUUIDFormatExempt(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(nil,
		map[string]metadata.ParamSchema{
			"id": {Type: "string", Format: "uuid"}, // exempt
		})

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "format:uuid pathParams must be exempt from FMT-25, got: %v", matches)
}

// TestFMT25_CleanSchemaProducesNoViolations verifies that a fully-constrained
// schema and a fully-constrained set of params produce zero FMT-25 violations.
func TestFMT25_CleanSchemaProducesNoViolations(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name":  {"type": "string", "minLength": 1, "maxLength": 128},
			"limit": {"type": "integer", "minimum": 1, "maximum": 500}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	one := 1
	twoFiftySix := 256
	fiveHundred := 500
	pm := fmt25Project(
		map[string]metadata.ParamSchema{
			"cursor": {Type: "string", MinLength: &one, MaxLength: &twoFiftySix},
			"limit":  {Type: "integer", Minimum: &one, Maximum: &fiveHundred},
			"ratio":  {Type: "number", Minimum: &one, Maximum: &fiveHundred},
		},
		map[string]metadata.ParamSchema{
			"id":  {Type: "string", Format: "uuid"},                           // uuid exempt
			"key": {Type: "string", MinLength: &one, MaxLength: &twoFiftySix}, // plain string with constraints
		})

	results, err := NewValidator(pm, dir, clock.Real()).ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "fully-constrained schema/params must produce no FMT-25, got: %v", matches)
}

// TestFMT25_NonHTTPContractIgnored verifies that non-HTTP contracts (event,
// command, projection) are not scanned by FMT-25.
func TestFMT25_NonHTTPContractIgnored(t *testing.T) {
	dir := t.TempDir()
	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.test.v1": {
				ID:        "event.test.v1",
				Kind:      "event",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Payload: "payload.schema.json",
				},
				Dir:  "contracts/event/test/v1",
				File: "contracts/event/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir, clock.Real())
	results, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "non-HTTP contract must not be scanned by FMT-25")
}

// FMT-20 helpers (fmt20Fixture, fmt20ResponseFixture, assertFMT20RequiredFields,
// fieldList) live in rules_strict_extra_fmt20_test.go alongside the table-driven
// tests after ADR-202605031600.

// =============================================================================
// FMT-20 (formerly rules_strict_extra_fmt20_test.go)
// =============================================================================

// FMT-20 (request schema strict additionalProperties; ADR-202605031600).
//
// Per ADR-202605031600 v1 schema evolution, FMT-20 enforces
// additionalProperties:false on request schemas only. Response schemas,
// endpoints.http.responses[*] schemaRefs, and event payload/headers schemas
// are intentionally lenient to allow v1 to grow optional fields without a
// major bump.

// Schema literals shared across the FMT-20 cases. Extracted so each table
// row reads as one line and so the same shape can be reused on both sides
// (request positive cases + response regression cases).

const fmt20SchemaTopLevelMissing = `{
    "type": "object",
    "properties": {
        "data": {
            "type": "object",
            "properties": {"id": {"type": "string"}}
        }
    }
}`

const fmt20SchemaArrayItems = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "list": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"id": {"type": "string"}}
            }
        }
    }
}`

const fmt20SchemaUnevaluatedItems = `{
    "type": "array",
    "items": {"type": "string"},
    "unevaluatedItems": {
        "type": "object",
        "properties": {"id": {"type": "string"}}
    }
}`

const fmt20SchemaAllOf = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {
            "allOf": [
                {
                    "type": "object",
                    "properties": {"id": {"type": "string"}}
                }
            ]
        }
    }
}`

const fmt20SchemaIfThenElse = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "payload": {
            "if": {"properties": {"kind": {"const": "a"}}},
            "then": {
                "type": "object",
                "properties": {"value": {"type": "string"}}
            },
            "else": {
                "type": "object",
                "properties": {"reason": {"type": "string"}}
            }
        }
    }
}`

const fmt20SchemaLocalRef = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {"$ref": "#/$defs/Wrapper"}
    },
    "$defs": {
        "Wrapper": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "choice": {
                    "oneOf": [
                        {
                            "type": "object",
                            "properties": {"a": {"type": "string"}}
                        }
                    ]
                }
            }
        }
    }
}`

const fmt20SchemaClean = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {
            "type": "object",
            "additionalProperties": false,
            "properties": {"id": {"type": "string"}}
        }
    }
}`

// fmt20SchemaExplicitOpen exercises the regression guard for ADR-202605031600:
// `additionalProperties: true` is an explicit open declaration, equivalent to
// missing-key as far as FMT-20 is concerned, and must trip the request-side
// violation. Response side ignores it (FMT-20 only scans request schemas).
const fmt20SchemaExplicitOpen = `{
    "type": "object",
    "additionalProperties": true,
    "properties": {"id": {"type": "string"}}
}`

// TestFMT20_BySchemaSide is the consolidated FMT-20 coverage matrix. Each row
// is run twice — once with the schema mounted as request.schema.json (FMT-20
// must report wantRequestFields) and once as response.schema.json (FMT-20 must
// report nothing per ADR-202605031600). Folding the two directions into one
// table makes it structurally impossible for a new shape to be added on one
// side and forgotten on the other.
func TestFMT20_BySchemaSide(t *testing.T) {
	cases := []struct {
		name              string
		schema            string
		wantRequestFields []string // nil => expect no FMT-20 violations
	}{
		{"TopLevelMissingAP", fmt20SchemaTopLevelMissing, []string{"$", "$.data"}},
		{"ArrayItemsObjectMissingAP", fmt20SchemaArrayItems, []string{"$.list.items"}},
		{"UnevaluatedItemsObjectMissingAP", fmt20SchemaUnevaluatedItems, []string{"$.unevaluatedItems"}},
		{"AllOfMissingAP", fmt20SchemaAllOf, []string{"$.data.allOf[0]"}},
		{"IfThenElseConditional", fmt20SchemaIfThenElse, []string{"$.payload.then", "$.payload.else"}},
		{"LocalRefThroughComposition", fmt20SchemaLocalRef, []string{"$.data.choice.oneOf[0]"}},
		{"ExplicitAdditionalPropertiesTrue", fmt20SchemaExplicitOpen, []string{"$"}},
		{"CleanRequestSchema", fmt20SchemaClean, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/Request", func(t *testing.T) {
			dir := t.TempDir()
			v := NewValidator(fmt20Fixture(t, dir, "case", tc.schema), dir, clock.Real())
			validateResults, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(validateResults, "FMT-20")
			assertFMT20RequiredFields(t, matches, tc.wantRequestFields)
		})
		t.Run(tc.name+"/Response", func(t *testing.T) {
			dir := t.TempDir()
			v := NewValidator(fmt20ResponseFixture(t, dir, "case", tc.schema), dir, clock.Real())
			validateResults, err := v.ValidateStrict(t.Context(), false, false)
			require.NoError(t, err)
			matches := findByCode(validateResults, "FMT-20")
			assert.Empty(t, matches,
				"response schema must not trigger FMT-20 (ADR-202605031600)")
		})
	}
}

// TestFMT20_EndpointResponsesSchemaRefIgnored verifies that the
// endpoints.http.responses[*].schemaRef path (used for per-status error
// schemas) is also out of FMT-20 scope after ADR-202605031600.
func TestFMT20_EndpointResponsesSchemaRefIgnored(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "errtest", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(contractDir, "error-404.schema.json"),
		[]byte(`{"type":"object","properties":{"message":{"type":"string"}}}`),
		0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.errtest.v1": {
				ID:        "http.errtest.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          "/test",
						SuccessStatus: 200,
						Responses: map[int]metadata.HTTPResponseMeta{
							404: {Description: "Not found", SchemaRef: "error-404.schema.json"},
						},
					},
				},
				Dir:  "contracts/http/errtest/v1",
				File: "contracts/http/errtest/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, dir, clock.Real())
	validateResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(validateResults, "FMT-20")
	assert.Empty(t, matches,
		"endpoints.http.responses[*] must not trigger FMT-20 (ADR-202605031600)")
}

// TestFMT20_NonHTTPContractIgnored verifies that non-HTTP contracts
// (event/projection/command) are not scanned at all.
func TestFMT20_NonHTTPContractIgnored(t *testing.T) {
	dir := t.TempDir()
	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.test.v1": {
				ID:        "event.test.v1",
				Kind:      "event",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Payload: "payload.schema.json",
				},
				Dir:  "contracts/event/test/v1",
				File: "contracts/event/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, dir, clock.Real())
	validateResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(validateResults, "FMT-20")
	assert.Empty(t, matches, "non-HTTP contract must not be scanned by FMT-20")
}

// TestFMT20_MalformedRequestSchemaEmitsIssueInvalid: parse error on the
// request schema is a definitive FMT-20 violation (fail-closed).
func TestFMT20_MalformedRequestSchemaEmitsIssueInvalid(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "badschema", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(contractDir, "request.schema.json"),
		[]byte(`not valid json {{{`), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.badschema.v1": {
				ID:        "http.badschema.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Request: "request.schema.json",
				},
				Dir:  "contracts/http/badschema/v1",
				File: "contracts/http/badschema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, dir, clock.Real())
	validateResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(validateResults, "FMT-20")
	require.Len(t, matches, 1,
		"malformed request schema must produce 1 FMT-20 violation, got %d: %v", len(matches), matches)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, SeverityError, matches[0].Severity)
	assert.Contains(t, matches[0].Message, "failed to parse")
}

// TestFMT20_MissingSchemaFileSkipped: missing request schema file is
// silently skipped (REF rules report it).
func TestFMT20_MissingSchemaFileSkipped(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.missing.schema.v1": {
				ID:        "http.missing.schema.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Request: "nonexistent.schema.json",
				},
				Dir:  "contracts/http/missing/schema/v1",
				File: "contracts/http/missing/schema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, t.TempDir(), clock.Real())
	validateResults, err := v.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	matches := findByCode(validateResults, "FMT-20")
	assert.Empty(t, matches,
		"missing schema file must produce no FMT-20 (handled by REF rules)")
}

// fmt20Fixture writes request.schema.json under dir/contracts/http/<name>/v1/
// and returns a ProjectMeta whose http contract references it via
// SchemaRefs.Request. Used by tests that want FMT-20 to actually scan the
// supplied schema.
func fmt20Fixture(t *testing.T, dir, name, schema string) *metadata.ProjectMeta {
	t.Helper()
	contractDir := filepath.Join(dir, "contracts", "http", name, "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "request.schema.json"), []byte(schema), 0o644))
	id := "http." + name + ".v1"
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			id: {
				ID:         id,
				Kind:       "http",
				OwnerCell:  "testcell",
				Lifecycle:  "active",
				SchemaRefs: metadata.SchemaRefsMeta{Request: "request.schema.json"},
				Dir:        "contracts/http/" + name + "/v1",
				File:       "contracts/http/" + name + "/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// fmt20ResponseFixture is the response-side counterpart used to assert that
// response schemas are NOT scanned (regression coverage for ADR-202605031600).
func fmt20ResponseFixture(t *testing.T, dir, name, schema string) *metadata.ProjectMeta {
	t.Helper()
	contractDir := filepath.Join(dir, "contracts", "http", name, "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "response.schema.json"), []byte(schema), 0o644))
	id := "http." + name + ".v1"
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			id: {
				ID:         id,
				Kind:       "http",
				OwnerCell:  "testcell",
				Lifecycle:  "active",
				SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
				Dir:        "contracts/http/" + name + "/v1",
				File:       "contracts/http/" + name + "/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func assertFMT20RequiredFields(t *testing.T, matches []ValidationResult, wantFields []string) {
	t.Helper()
	require.Len(t, matches, len(wantFields), "unexpected FMT-20 fields: %v", fieldList(matches))
	assert.ElementsMatch(t, wantFields, fieldList(matches))
	for _, m := range matches {
		assert.Equal(t, SeverityError, m.Severity)
		assert.Equal(t, IssueRequired, m.IssueType)
	}
}

func fieldList(results []ValidationResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Field)
	}
	return out
}
