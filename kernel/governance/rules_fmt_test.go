package governance

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestFMT13_MissingEndpointsHTTP verifies that an HTTP contract without
// endpoints.http produces a FMT-13 SeverityError.
func TestFMT13_MissingEndpointsHTTP(t *testing.T) {
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
				File:             "cells/accesscore/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.test.missing.v1": {
				ID:               "http.test.missing.v1",
				Kind:             "http",
				OwnerCell:        "accesscore",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					// HTTP is nil — missing endpoints.http
				},
				Dir:  "contracts/http/test/missing/v1",
				File: "contracts/http/test/missing/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT13()

	var fmt13Errors []ValidationResult
	for _, r := range results {
		if r.Code == codeFMT13 && r.Severity == SeverityError {
			fmt13Errors = append(fmt13Errors, r)
		}
	}

	if len(fmt13Errors) != 1 {
		t.Fatalf("FMT-13 missing endpoints.http: expected 1 error, got %d: %v", len(fmt13Errors), fmt13Errors)
	}
	if !strings.Contains(fmt13Errors[0].Message, "endpoints.http") {
		t.Errorf("FMT-13: expected 'endpoints.http' in message, got: %s", fmt13Errors[0].Message)
	}
	if !strings.Contains(fmt13Errors[0].Message, "method:") {
		t.Errorf("FMT-13: expected 'method:' YAML template in message, got: %s", fmt13Errors[0].Message)
	}
}

// TestFMT13_NonHTTPContractSkipped verifies that non-HTTP contracts are not
// flagged by FMT-13 even when they have no endpoints.http block.
func TestFMT13_NonHTTPContractSkipped(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.test.created.v1": {
				ID:               "event.test.created.v1",
				Kind:             "event",
				OwnerCell:        "accesscore",
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "accesscore",
					Subscribers: []string{"auditcore"},
				},
				Dir:  "contracts/event/test/created/v1",
				File: "contracts/event/test/created/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT13()

	for _, r := range results {
		if r.Code == codeFMT13 {
			t.Errorf("FMT-13: unexpected finding on non-HTTP contract: %v", r)
		}
	}
}

// --- FMT-26 (auth.public and auth.passwordResetExempt mutually exclusive) ---

// TestFMT26_BothTrue verifies that a contract declaring both auth.public:true
// and auth.passwordResetExempt:true is rejected by FMT-26.
func TestFMT26_BothTrue(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.bad.v1": {
				ID:               "http.auth.bad.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/auth/bad",
						SuccessStatus: 200,
						Auth: metadata.HTTPAuthMeta{
							Public:              true,
							PasswordResetExempt: true,
						},
					},
				},
				Dir:  "contracts/http/auth/bad/v1",
				File: "contracts/http/auth/bad/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT26()

	var fmt26Errors []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-26" && r.Severity == SeverityError {
			fmt26Errors = append(fmt26Errors, r)
		}
	}

	if len(fmt26Errors) == 0 {
		t.Fatal("FMT-26: expected error when both auth.public and auth.passwordResetExempt are true, got none")
	}
	found := false
	for _, r := range fmt26Errors {
		if r.Field == "endpoints.http.auth" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("FMT-26: expected finding on field 'endpoints.http.auth', got: %v", fmt26Errors)
	}
}

// TestFMT26_OnlyPublic verifies that a contract with only auth.public:true passes FMT-26.
func TestFMT26_OnlyPublic(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:               "http.auth.login.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/auth/sessions",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Public: true,
						},
					},
				},
				Dir:  "contracts/http/auth/login/v1",
				File: "contracts/http/auth/login/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT26()

	for _, r := range results {
		if r.Code == "FMT-26" {
			t.Errorf("FMT-26: unexpected finding for auth.public-only contract: %v", r)
		}
	}
}

// TestFMT26_OnlyPasswordResetExempt verifies that a contract with only
// auth.passwordResetExempt:true passes FMT-26.
func TestFMT26_OnlyPasswordResetExempt(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.session.delete.v1": {
				ID:               "http.auth.session.delete.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "DELETE",
						Path:          "/api/v1/auth/sessions/{sessionId}",
						SuccessStatus: 204,
						Auth: metadata.HTTPAuthMeta{
							PasswordResetExempt: true,
						},
					},
				},
				Dir:  "contracts/http/auth/session/delete/v1",
				File: "contracts/http/auth/session/delete/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT26()

	for _, r := range results {
		if r.Code == "FMT-26" {
			t.Errorf("FMT-26: unexpected finding for passwordResetExempt-only contract: %v", r)
		}
	}
}

// TestFMT26_NeitherSet verifies that a contract with no auth overrides passes FMT-26.
func TestFMT26_NeitherSet(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.users.list.v1": {
				ID:               "http.users.list.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          "/api/v1/users",
						SuccessStatus: 200,
					},
				},
				Dir:  "contracts/http/users/list/v1",
				File: "contracts/http/users/list/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT26()

	for _, r := range results {
		if r.Code == "FMT-26" {
			t.Errorf("FMT-26: unexpected finding for contract with no auth overrides: %v", r)
		}
	}
}

// TestFMT13_HTTPContractWithEndpoints verifies that an HTTP contract with
// a valid endpoints.http block produces no FMT-13 error.
func TestFMT13_HTTPContractWithEndpoints(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.test.ok.v1": {
				ID:               "http.test.ok.v1",
				Kind:             "http",
				OwnerCell:        "accesscore",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          "/api/v1/test",
						SuccessStatus: 200,
					},
				},
				Dir:  "contracts/http/test/ok/v1",
				File: "contracts/http/test/ok/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT13()

	for _, r := range results {
		if r.Code == codeFMT13 && r.Severity == SeverityError {
			t.Errorf("FMT-13: unexpected error for contract with valid endpoints.http: %v", r)
		}
	}
}

// --- FMT-27 (auth.public/bootstrap/passwordResetExempt three-way mutually exclusive) ---
// These tests are RED until validateFMT27 is implemented in Batch 1 / Agent-B.

// TestFMT27_PublicAndBootstrapBothTrue verifies that a contract declaring both
// auth.public:true and auth.bootstrap:true is rejected by FMT-27.
// RED: validateFMT27 does not yet exist.
func TestFMT27_PublicAndBootstrapBothTrue(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.setup.admin.v1": {
				ID:               "http.auth.setup.admin.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/access/setup/admin",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Public:    true,
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/auth/setup/admin/v1",
				File: "contracts/http/auth/setup/admin/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())

	// validateFMT27 does not exist yet — RED: calling a method that panics
	// or returns no results.
	results := v.validateFMT27()

	var fmt27Errors []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-27" && r.Severity == SeverityError {
			fmt27Errors = append(fmt27Errors, r)
		}
	}

	if len(fmt27Errors) == 0 {
		t.Fatal("FMT-27: expected error when both auth.public and auth.bootstrap are true, got none — " +
			"validateFMT27 not yet implemented (Batch 1 Agent-B)")
	}
}

// TestFMT27_BootstrapAndPasswordResetExemptBothTrue verifies that
// auth.bootstrap:true + auth.passwordResetExempt:true is rejected by FMT-27.
// RED: validateFMT27 does not yet exist.
func TestFMT27_BootstrapAndPasswordResetExemptBothTrue(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.bad.v1": {
				ID:               "http.auth.bad.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/access/setup/admin",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Bootstrap:           true,
							PasswordResetExempt: true,
						},
					},
				},
				Dir:  "contracts/http/auth/bad/v1",
				File: "contracts/http/auth/bad/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT27()

	var fmt27Errors []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-27" && r.Severity == SeverityError {
			fmt27Errors = append(fmt27Errors, r)
		}
	}

	if len(fmt27Errors) == 0 {
		t.Fatal("FMT-27: expected error when both auth.bootstrap and auth.passwordResetExempt are true, got none — " +
			"validateFMT27 not yet implemented (Batch 1 Agent-B)")
	}
}

// --- FMT-28 (auth.bootstrap:true only allowed on IsBootstrapPath paths) ---

// TestFMT28_BootstrapOnNonSetupAdminPath verifies that auth.bootstrap:true on a
// path that does not match IsBootstrapPath is rejected by FMT-28.
func TestFMT28_BootstrapOnNonSetupAdminPath(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.some.endpoint.v1": {
				ID:               "http.some.endpoint.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "somecore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/some/other/endpoint",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/some/endpoint/v1",
				File: "contracts/http/some/endpoint/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT28()

	var fmt28Errors []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-28" && r.Severity == SeverityError {
			fmt28Errors = append(fmt28Errors, r)
		}
	}

	if len(fmt28Errors) == 0 {
		t.Fatal("FMT-28: expected error when auth.bootstrap:true on path not matching IsBootstrapPath, got none")
	}
}

// TestFMT28_BootstrapOnSubstringMatchPath verifies that a path like
// /api/v1/setup/admin/foo (substring match but not exact segment match)
// is rejected by FMT-28. This guards against the old strings.Contains approach.
func TestFMT28_BootstrapOnSubstringMatchPath(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.setup.admin.extra.v1": {
				ID:               "http.setup.admin.extra.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "somecore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/setup/admin/foo",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/setup/admin/extra/v1",
				File: "contracts/http/setup/admin/extra/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT28()

	var fmt28Errors []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-28" && r.Severity == SeverityError {
			fmt28Errors = append(fmt28Errors, r)
		}
	}

	if len(fmt28Errors) == 0 {
		t.Fatal("FMT-28: expected error for path /api/v1/setup/admin/foo (substring match, not exact segment); " +
			"IsBootstrapPath must reject paths with trailing segments")
	}
}

// TestFMT28_BootstrapOnSetupAdminPath verifies that auth.bootstrap:true on a
// path matching IsBootstrapPath is allowed by FMT-28 (no error).
func TestFMT28_BootstrapOnSetupAdminPath(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.setup.admin.v1": {
				ID:               "http.auth.setup.admin.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/access/setup/admin",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/auth/setup/admin/v1",
				File: "contracts/http/auth/setup/admin/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT28()

	for _, r := range results {
		if r.Code == "FMT-28" && r.Severity == SeverityError {
			t.Errorf("FMT-28: unexpected error for auth.bootstrap on IsBootstrapPath-valid path: %v", r)
		}
	}
}

// --- FMT-29: assembly owner.team and owner.role required ---

// buildFMT29Project returns a minimal ProjectMeta containing one assembly with
// the given owner fields.
func buildFMT29Project(team, role string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": {
				ID:    "testasm",
				Cells: []string{},
				Owner: metadata.OwnerMeta{Team: team, Role: role},
				Dir:   "testasm",
				File:  "assemblies/testasm/assembly.yaml",
			},
		},
	}
}

// TestFMT29_MissingOwnerTeam verifies that an assembly without owner.team
// produces an FMT-29 SeverityError finding.
func TestFMT29_MissingOwnerTeam(t *testing.T) {
	project := buildFMT29Project("", "assembly-owner")
	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT29()

	var got []ValidationResult
	for _, r := range results {
		if r.Code == codeFMT29 && r.Severity == SeverityError && r.Field == "owner.team" {
			got = append(got, r)
		}
	}
	if len(got) == 0 {
		t.Errorf("FMT-29: expected finding for missing owner.team, got 0 findings")
	}
}

// TestFMT29_MissingOwnerRole verifies that an assembly without owner.role
// produces an FMT-29 SeverityError finding.
func TestFMT29_MissingOwnerRole(t *testing.T) {
	project := buildFMT29Project("platform", "")
	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT29()

	var got []ValidationResult
	for _, r := range results {
		if r.Code == codeFMT29 && r.Severity == SeverityError && r.Field == "owner.role" {
			got = append(got, r)
		}
	}
	if len(got) == 0 {
		t.Errorf("FMT-29: expected finding for missing owner.role, got 0 findings")
	}
}

// TestFMT29_FullOwner verifies that an assembly with both owner.team and
// owner.role set produces 0 FMT-29 findings.
func TestFMT29_FullOwner(t *testing.T) {
	project := buildFMT29Project("platform", "assembly-owner")
	v := NewValidator(project, "", clock.Real())
	results := v.validateFMT29()

	for _, r := range results {
		if r.Code == codeFMT29 {
			t.Errorf("FMT-29: unexpected finding for complete owner: %v", r)
		}
	}
}
