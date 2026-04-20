package governance

import (
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
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// Non-strict: kebab dir in slice ID should produce warning, not error.
	v := NewValidator(project, "")
	results := v.ValidateStrict(false)
	hasKebabError := false
	for _, r := range results {
		if r.Code == "FMT-16" && r.Severity == SeverityError {
			hasKebabError = true
		}
	}
	if hasKebabError {
		t.Error("non-strict mode should not produce FMT-16 error for kebab slice ID")
	}

	// Strict mode: kebab dir in slice key should produce error.
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
