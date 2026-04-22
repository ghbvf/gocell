package governance

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestStrictValidator_KebabDirDisallowed verifies that --strict mode upgrades
// kebab-case slice directory warnings to errors.
func TestStrictValidator_KebabDirDisallowed(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-login": {
				ID:            "session-login",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/access-core/slices/session-login/**",
				},
				Dir:     "session-login",
				CellDir: "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// Non-strict: kebab dir in slice dir should produce warning, not error.
	v := NewValidator(project, "")
	results := v.ValidateStrict(false)
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
	results = v.ValidateStrict(true)
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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				// First entry points to wrong (kebab) path.
				AllowedFiles: []string{
					"cells/access-core/slices/session-login/**",
					"cells/access-core/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// Non-strict: no FMT-17 error.
	v := NewValidator(project, "")
	results := v.ValidateStrict(false)
	for _, r := range results {
		if r.Code == "FMT-17" && r.Severity == SeverityError {
			t.Error("non-strict mode should not produce FMT-17 error")
		}
	}

	// Strict: allowedFiles first entry mismatch should be error.
	results = v.ValidateStrict(true)
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

// TestValidateStrictFailFast_ShortCircuitsOnBaseError verifies that when the
// base ValidateFailFast finds an error, ValidateStrictFailFast returns
// immediately without appending FMT-16 or FMT-17 results.
func TestValidateStrictFailFast_ShortCircuitsOnBaseError(t *testing.T) {
	// A project whose cell has a missing required field (schema.primary) so
	// that standard rules fire an error, plus a kebab slice that would trigger
	// FMT-16 in strict mode.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				// schema.primary intentionally empty → triggers FMT-01 error.
				Schema: metadata.SchemaMeta{Primary: ""},
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-login": {
				ID:            "session-login",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{"cells/access-core/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.ValidateStrictFailFast()

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

// TestValidateStrictFailFast_RunsFMT16FMT17WhenNoBaseError verifies that when
// the base pass finds no errors, ValidateStrictFailFast appends FMT-16/17 just
// like ValidateStrict(true) would.
func TestValidateStrictFailFast_RunsFMT16FMT17WhenNoBaseError(t *testing.T) {
	// L1 cell with no contractUsages — passes all standard rules while the
	// slice key uses a kebab-case directory that strict mode (FMT-16) must flag.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-login": {
				ID:             "session-login",
				BelongsToCell:  "access-core",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-login.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/access-core/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.ValidateStrictFailFast()

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

	v := NewValidator(project, "")
	strictResults := v.ValidateStrict(true)
	baseResults := v.Validate()

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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/access-core/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	nonStrictResults := v.ValidateStrict(false)
	baseResults := v.Validate()

	// Build comparable fingerprint: sorted list of "code:severity" strings.
	fingerprint := func(results []ValidationResult) map[string]int {
		m := make(map[string]int)
		for _, r := range results {
			key := r.Code + ":" + string(r.Severity)
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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/sessionlogin": {
				ID:            "sessionlogin",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{"contract.http.auth.login.v1.serve"},
				},
				AllowedFiles: []string{
					"cells/access-core/slices/sessionlogin/**",
				},
				Dir:     "sessionlogin",
				CellDir: "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.ValidateStrict(true)
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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
				Dir:              "access-core",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			// Map key is "access-core/sessionlogin" (yaml id), but the
			// walked directory is the kebab-case "session-login".
			"access-core/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "access-core",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/access-core/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.ValidateStrict(true)

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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
				Dir:              "access-core",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "access-core",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/access-core/slices/sessionlogin/**"},
				Dir:          "session-login", // disk says kebab, yaml says no-dash
				CellDir:      "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.Validate() // REF-05 is a standard rule, not strict-only

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

// TestStrictValidator_FMTC1_KebabCellID verifies that FMT-C1 flags cell.yaml
// ids containing '-' (kebab-case) in strict mode and is silent otherwise.
func TestStrictValidator_FMTC1_KebabCellID(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.startup"}},
				Dir:              "accesscore", // dir already no-dash, but id still dash
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")

	// Non-strict: silent.
	for _, r := range v.ValidateStrict(false) {
		if r.Code == "FMT-C1" {
			t.Errorf("non-strict mode must not emit FMT-C1: %s", r.Message)
		}
	}

	// Strict: must fire FMT-C1.
	var got bool
	for _, r := range v.ValidateStrict(true) {
		if r.Code == "FMT-C1" && r.Severity == SeverityError {
			got = true
		}
	}
	if !got {
		t.Error("strict mode should produce FMT-C1 error for kebab-case cell id")
	}
}

// TestStrictValidator_FMTA1_KebabAssemblyID verifies FMT-A1 flags
// assembly.yaml ids containing '-' in strict mode.
func TestStrictValidator_FMTA1_KebabAssemblyID(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"core-bundle": {
				ID:    "core-bundle",
				Cells: []string{},
				Build: metadata.BuildMeta{Entrypoint: "cmd/corebundle/main.go", Binary: "corebundle"},
				Dir:   "corebundle",
			},
		},
	}

	v := NewValidator(project, "")

	for _, r := range v.ValidateStrict(false) {
		if r.Code == "FMT-A1" {
			t.Errorf("non-strict mode must not emit FMT-A1: %s", r.Message)
		}
	}

	var got bool
	for _, r := range v.ValidateStrict(true) {
		if r.Code == "FMT-A1" && r.Severity == SeverityError {
			got = true
		}
	}
	if !got {
		t.Error("strict mode should produce FMT-A1 error for kebab-case assembly id")
	}
}

// TestStrictValidator_FMT16_KebabCellDir verifies FMT-16 flags kebab cell
// directories (not just slices).
func TestStrictValidator_FMT16_KebabCellDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_accesscore"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.startup"}},
				Dir:              "access-core", // id clean but dir kebab
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")

	var got bool
	for _, r := range v.ValidateStrict(true) {
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
// assembly directories.
func TestStrictValidator_FMT16_KebabAssemblyDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"corebundle": {
				ID:    "corebundle",
				Cells: []string{},
				Build: metadata.BuildMeta{Entrypoint: "cmd/corebundle/main.go", Binary: "corebundle"},
				Dir:   "core-bundle", // id clean but dir kebab
			},
		},
	}

	v := NewValidator(project, "")

	var got bool
	for _, r := range v.ValidateStrict(true) {
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

	v := NewValidator(project, "")
	for _, r := range v.ValidateStrict(true) {
		if r.Code == "FMT-C1" || r.Code == "FMT-A1" {
			t.Errorf("clean no-dash project should not produce %s: %s", r.Code, r.Message)
		}
		if r.Code == "FMT-16" && (strings.Contains(r.Message, "cell") || strings.Contains(r.Message, "assembly")) {
			t.Errorf("clean no-dash project should not produce FMT-16 for cell/assembly: %s", r.Message)
		}
	}
}

// FMT-17 must compare allowedFiles against the real disk directory, not
// against a directory synthesised from slice.id. Before the Dir field was
// introduced, an allowedFiles entry that echoed the (faked) id path would
// silently match and let the real kebab-case directory slip through.
func TestStrictValidator_FMT17_AllowedFilesAgainstRealDir(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
				Dir:              "access-core",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/sessionlogin": {
				ID:             "sessionlogin",
				BelongsToCell:  "access-core",
				ContractUsages: []metadata.ContractUsage{},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.sessionlogin.service"},
					Contract: []string{},
				},
				// allowedFiles points to the (no-dash) id path, lying about
				// the real directory on disk.
				AllowedFiles: []string{"cells/access-core/slices/sessionlogin/**"},
				Dir:          "session-login",
				CellDir:      "access-core",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "")
	results := v.ValidateStrict(true)

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
