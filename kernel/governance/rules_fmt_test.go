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

// --- FMT-27 (HTTP auth bool mutex) ---
// All per-pair / per-bool subtests are subsumed by TestFMT27AuthBoolMatrix
// (32-combo matrix sharing metadata.AuthComboLegal as oracle, defined below
// alongside the fmt27ProjectWithAuth fixture). The matrix is the sole entry
// point; happy-path documentation lives in contract_schema_test.go where
// full contract YAML samples drive the schema layer.

func fmt27ProjectWithAuth(auth metadata.HTTPAuthMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.mode.fixture.v1": {
				ID:               "http.auth.mode.fixture.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/internal/v1/access/auth-mode-fixture",
						SuccessStatus: 200,
						Auth:          auth,
					},
				},
				Dir:  "contracts/http/auth/mode/fixture/v1",
				File: "contracts/http/auth/mode/fixture/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestFMT27ErrorDiagnostics locks the FMT-27 message format so contract
// authors get actionable diagnostics: the message must (1) name every auth
// field currently set to true, (2) signal the conflict ("incompatible"),
// and (3) carry the fix hint ("Set at most one"). Without these assertions
// the message text could regress silently.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestFMT27ErrorDiagnostics(t *testing.T) {
	cases := []struct {
		name     string
		auth     metadata.HTTPAuthMeta
		mustName []string
	}{
		{
			name:     "public+bootstrap",
			auth:     metadata.HTTPAuthMeta{Public: true, Bootstrap: true},
			mustName: []string{"auth.public", "auth.bootstrap"},
		},
		{
			name:     "clientsOnly+serviceOwned",
			auth:     metadata.HTTPAuthMeta{ClientsOnly: true, ServiceOwned: true},
			mustName: []string{"auth.serviceOwned", "auth.clientsOnly"},
		},
		{
			name: "all four core modes",
			auth: metadata.HTTPAuthMeta{
				Public:              true,
				PasswordResetExempt: true,
				Bootstrap:           true,
				ClientsOnly:         true,
			},
			mustName: []string{"auth.public", "auth.passwordResetExempt", "auth.bootstrap", "auth.clientsOnly"},
		},
		{
			name:     "serviceOwned+bootstrap",
			auth:     metadata.HTTPAuthMeta{ServiceOwned: true, Bootstrap: true},
			mustName: []string{"auth.serviceOwned", "auth.bootstrap"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := NewValidator(fmt27ProjectWithAuth(tc.auth), "", clock.Real())
			matches := findByCode(v.validateFMT27(), codeFMT27)
			assertFMT27Diagnostic(t, matches, tc.mustName)
		})
	}
}

// assertFMT27Diagnostic asserts that a single FMT-27 violation was produced
// and that its message names every field in mustName plus the conflict
// keyword and fix hint. Extracted from TestFMT27ErrorDiagnostics to keep the
// table-driven loop body within cognitive-complexity bounds.
func assertFMT27Diagnostic(t *testing.T, matches []ValidationResult, mustName []string) {
	t.Helper()
	if len(matches) != 1 {
		t.Fatalf("FMT-27: expected exactly 1 violation, got %d: %v", len(matches), matches)
	}
	msg := matches[0].Message
	for _, field := range mustName {
		if !strings.Contains(msg, field) {
			t.Errorf("FMT-27 diagnostic missing %q in message: %s", field, msg)
		}
	}
	if !strings.Contains(msg, "incompatible") {
		t.Errorf("FMT-27 diagnostic missing 'incompatible' keyword in message: %s", msg)
	}
	if !strings.Contains(msg, "Set at most one") {
		t.Errorf("FMT-27 diagnostic missing 'Set at most one' fix hint in message: %s", msg)
	}
}

// TestFMT27AuthBoolMatrix enumerates all 32 combinations of the 5 auth bool
// fields and asserts validateFMT27's behavior against metadata.LegalAuthComboNames
// — the hand-maintained whitelist that is independent of AuthComboLegal. Using
// the whitelist (rather than AuthComboLegal) ensures this test detects
// divergence in the governance delegation chain rather than merely confirming
// that governance and oracle move in lock-step.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestFMT27AuthBoolMatrix(t *testing.T) {
	metadata.IterateAuthBoolCombos(func(auth metadata.HTTPAuthMeta, name string) {
		t.Run(name, func(t *testing.T) {
			v := NewValidator(fmt27ProjectWithAuth(auth), "", clock.Real())
			matches := findByCode(v.validateFMT27(), codeFMT27)
			_, expectedLegal := metadata.LegalAuthComboNames[name]
			if expectedLegal && len(matches) != 0 {
				t.Errorf("FMT-27 rejected legal combo %s: %v", name, matches)
			}
			if !expectedLegal && len(matches) == 0 {
				t.Errorf("FMT-27 accepted illegal combo %s; expected reject per LegalAuthComboNames", name)
			}
		})
	})
}

// --- FMT-28 (HTTP auth mode placement/shape constraints) ---

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

func TestFMT28_ClientsOnlyOnNonInternalPath(t *testing.T) {
	project := fmt28ProjectWithClientsOnlyPath("/api/v1/sample/list", []string{"edge-bff"})
	v := NewValidator(project, "", clock.Real())

	matches := findByCode(v.validateFMT28(), codeFMT28)
	if len(matches) == 0 {
		t.Fatal("FMT-28: expected error when auth.clientsOnly:true uses a non-internal path, got none")
	}
}

func TestFMT28_ClientsOnlyWithEmptyClients(t *testing.T) {
	project := fmt28ProjectWithClientsOnlyPath("/internal/v1/sample/list", nil)
	v := NewValidator(project, "", clock.Real())

	matches := findByCode(v.validateFMT28(), codeFMT28)
	if len(matches) == 0 {
		t.Fatal("FMT-28: expected error when auth.clientsOnly:true has empty endpoints.clients, got none")
	}
}

func TestFMT28_ClientsOnlyInternalPathWithClients(t *testing.T) {
	project := fmt28ProjectWithClientsOnlyPath("/internal/v1/sample/list", []string{"edge-bff"})
	v := NewValidator(project, "", clock.Real())

	matches := findByCode(v.validateFMT28(), codeFMT28)
	if len(matches) != 0 {
		t.Fatalf("FMT-28: expected auth.clientsOnly:true on internal path with clients to pass, got: %v", matches)
	}
}

func fmt28ProjectWithClientsOnlyPath(path string, clients []string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.sample.list.v1": {
				ID:               "http.sample.list.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "samplecore",
					Clients: clients,
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          path,
						SuccessStatus: 200,
						Auth: metadata.HTTPAuthMeta{
							ClientsOnly: true,
						},
					},
				},
				Dir:  "contracts/http/sample/list/v1",
				File: "contracts/http/sample/list/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
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

// --- FMT-31: /internal/v1/* HTTP contracts must declare non-empty endpoints.clients ---

// fmt31Project returns a minimal *metadata.ProjectMeta with one HTTP contract
// shaped by the test inputs. lifecycle="" defaults to "active". When httpNil
// is true, Endpoints.HTTP is left nil (covers FMT-07 boundary). kind is the
// contract Kind ("http", "event", ...).
func fmt31Project(kind, path string, clients []string, lifecycle string, httpNil bool) *metadata.ProjectMeta {
	if lifecycle == "" {
		lifecycle = "active"
	}
	endpoints := metadata.EndpointsMeta{
		Server:  "samplecore",
		Clients: clients,
	}
	if !httpNil {
		endpoints.HTTP = &metadata.HTTPTransportMeta{
			Method:        "GET",
			Path:          path,
			SuccessStatus: 200,
		}
	}
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.sample.list.v1": {
				ID:               "http.sample.list.v1",
				Kind:             kind,
				ConsistencyLevel: "L1",
				Lifecycle:        lifecycle,
				Endpoints:        endpoints,
				Dir:              "contracts/http/sample/list/v1",
				File:             "contracts/http/sample/list/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func TestFMT31_InternalPathWithClients_OK(t *testing.T) {
	project := fmt31Project("http", "/internal/v1/foo/list", []string{"edgecell"}, "", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 0 {
		t.Fatalf("FMT-31: expected 0 findings for internal path with declared clients, got: %v", matches)
	}
}

func TestFMT31_InternalPathEmptyClients_Error(t *testing.T) {
	project := fmt31Project("http", "/internal/v1/foo/list", nil, "", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 1 {
		t.Fatalf("FMT-31: expected 1 finding for internal path with empty clients, got %d: %v", len(matches), matches)
	}
	if matches[0].Field != "endpoints.clients" {
		t.Errorf("FMT-31: expected Field=endpoints.clients, got %q", matches[0].Field)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-31: expected SeverityError, got %v", matches[0].Severity)
	}
}

func TestFMT31_BareInternalRoot_Error(t *testing.T) {
	// metadata.IsInternalHTTPPath also matches the bare "/internal/v1" (no
	// trailing slash) edge — verify FMT-31 inherits that semantics rather than
	// inlining strings.HasPrefix.
	project := fmt31Project("http", "/internal/v1", nil, "", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 1 {
		t.Fatalf("FMT-31: expected 1 finding for bare /internal/v1 with empty clients, got %d", len(matches))
	}
}

func TestFMT31_NonInternalPathEmptyClients_OK(t *testing.T) {
	// FMT-31 is unidirectional: non-internal paths with empty clients are out
	// of its scope. The inverse direction (non-internal must have empty clients)
	// is enforced at runtime by kernel/contractspec.ContractSpec.validateHTTP,
	// not at the YAML governance layer (endpoints.clients is semantically
	// polymorphic — clientsOnly auth declares it on non-internal paths).
	project := fmt31Project("http", "/api/v1/sample/list", nil, "", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 0 {
		t.Fatalf("FMT-31: expected 0 findings for non-internal path, got: %v", matches)
	}
}

func TestFMT31_NonHTTPKind_Skipped(t *testing.T) {
	project := fmt31Project("event", "", nil, "", true)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 0 {
		t.Fatalf("FMT-31: expected 0 findings for non-http kind, got: %v", matches)
	}
}

func TestFMT31_HTTPNil_Skipped(t *testing.T) {
	// HTTP=nil on kind=http is FMT-07's domain; FMT-31 must not duplicate.
	project := fmt31Project("http", "", nil, "", true)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 0 {
		t.Fatalf("FMT-31: expected 0 findings when Endpoints.HTTP is nil, got: %v", matches)
	}
}

func TestFMT31_DeprecatedLifecycle_StillEnforced(t *testing.T) {
	// A deprecated internal endpoint without clients is still a security
	// liability (anyone with service-token could call it). Mirror the runtime
	// validateHTTP check which has no lifecycle gate.
	project := fmt31Project("http", "/internal/v1/foo/list", nil, "deprecated", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 1 {
		t.Fatalf("FMT-31: expected 1 finding for deprecated internal with empty clients, got %d", len(matches))
	}
}

func TestFMT31_DraftLifecycle_StillEnforced(t *testing.T) {
	// Same rationale as the deprecated case: lifecycle is not a gate. A draft
	// internal endpoint missing its caller allowlist would still serve traffic
	// in any environment that runs it, so FMT-31 fires regardless of lifecycle.
	project := fmt31Project("http", "/internal/v1/foo/list", nil, "draft", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 1 {
		t.Fatalf("FMT-31: expected 1 finding for draft internal with empty clients, got %d", len(matches))
	}
}

func TestFMT31_InternalV10_SubstringTrap(t *testing.T) {
	// Verifies metadata.IsInternalHTTPPath does not match "/internal/v10/x"
	// (substring trap); FMT-31 must inherit that boundary discipline.
	project := fmt31Project("http", "/internal/v10/x", nil, "", false)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 0 {
		t.Fatalf("FMT-31: expected 0 findings for /internal/v10/x (not internal-v1), got: %v", matches)
	}
}

func TestFMT31_MultipleContracts_Aggregate(t *testing.T) {
	project := fmt31Project("http", "/internal/v1/foo", nil, "", false)
	// Add a second internal contract; Clients is intentionally omitted (zero
	// value) so FMT-31 must flag both this and the contract from
	// fmt31Project above, producing exactly 2 aggregated findings.
	project.Contracts["http.sample.detail.v1"] = &metadata.ContractMeta{
		ID:               "http.sample.detail.v1",
		Kind:             "http",
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server: "samplecore",
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/internal/v1/bar",
				SuccessStatus: 200,
			},
		},
		Dir:  "contracts/http/sample/detail/v1",
		File: "contracts/http/sample/detail/v1/contract.yaml",
	}
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT31(), codeFMT31)
	if len(matches) != 2 {
		t.Fatalf("FMT-31: expected 2 findings (one per offending contract), got %d: %v", len(matches), matches)
	}
}

// --- FMT-32: serviceOwned=true contracts must declare endpoints.http.ownership ---

// fmt32Project builds a minimal *metadata.ProjectMeta with one HTTP contract.
// serviceOwned controls whether auth.ServiceOwned is true. ownership may be
// nil (block absent) or non-nil (block present, possibly with empty fields).
// pathParams declares route path parameters for referential integrity checks.
func fmt32Project(
	serviceOwned bool,
	ownership *metadata.HTTPOwnershipMeta,
	pathParams map[string]metadata.ParamSchema,
) *metadata.ProjectMeta {
	h := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/resource/{id}",
		SuccessStatus: 200,
		PathParams:    pathParams,
		Auth:          metadata.HTTPAuthMeta{ServiceOwned: serviceOwned},
		Ownership:     ownership,
	}
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.resource.get.v1": {
				ID:               "http.resource.get.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "samplecore",
					Clients: []string{"edgecell"},
					HTTP:    h,
				},
				Dir:  "contracts/http/resource/get/v1",
				File: "contracts/http/resource/get/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestFMT32_OwnershipDeclarationRequired is the main table-driven test for
// FMT-32. It covers: skip non-serviceOwned, valid full block, missing block,
// empty subjectPath, empty resourcePath, invalid DSL, path param referential
// integrity, both paths invalid, non-http kind skip, HTTP=nil skip, and
// multiple-contract aggregation.
func TestFMT32_OwnershipDeclarationRequired(t *testing.T) {
	sidParam := map[string]metadata.ParamSchema{
		"id": {Type: "string"},
	}

	tests := []struct {
		name      string
		project   *metadata.ProjectMeta
		wantCount int
		wantIssue IssueType
		wantField string
	}{
		{
			name:      "serviceOwned=false skip",
			project:   fmt32Project(false, nil, sidParam),
			wantCount: 0,
		},
		{
			name: "valid complete block",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "ctx.userID",
				ResourcePath: "path.id.ownerID",
			}, sidParam),
			wantCount: 0,
		},
		{
			name:      "missing ownership block",
			project:   fmt32Project(true, nil, sidParam),
			wantCount: 1,
			wantIssue: IssueRequired,
			wantField: "endpoints.http.ownership",
		},
		{
			name: "subjectPath empty",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "",
				ResourcePath: "path.id.ownerID",
			}, sidParam),
			wantCount: 1,
			wantIssue: IssueRequired,
			wantField: "endpoints.http.ownership.subjectPath",
		},
		{
			name: "resourcePath empty",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "ctx.userID",
				ResourcePath: "",
			}, sidParam),
			wantCount: 1,
			wantIssue: IssueRequired,
			wantField: "endpoints.http.ownership.resourcePath",
		},
		{
			name: "subjectPath invalid DSL",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "foo bar",
				ResourcePath: "path.id.ownerID",
			}, sidParam),
			wantCount: 1,
			wantIssue: IssueInvalid,
			wantField: "endpoints.http.ownership.subjectPath",
		},
		{
			name: "resourcePath param not in pathParams",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "ctx.userID",
				ResourcePath: "path.sid.ownerID",
			}, sidParam), // pathParams only has "id", not "sid"
			wantCount: 1,
			wantIssue: IssueInvalid,
			wantField: "endpoints.http.ownership.resourcePath",
		},
		{
			name: "both paths invalid",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "bad path",
				ResourcePath: "bad path",
			}, sidParam),
			wantCount: 2,
		},
		{
			name: "kind=event skip",
			project: func() *metadata.ProjectMeta {
				p := fmt32Project(false, nil, nil)
				p.Contracts["http.resource.get.v1"].Kind = "event"
				return p
			}(),
			wantCount: 0,
		},
		{
			name: "HTTP=nil skip",
			project: func() *metadata.ProjectMeta {
				p := fmt32Project(false, nil, nil)
				p.Contracts["http.resource.get.v1"].Endpoints.HTTP = nil
				return p
			}(),
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := NewValidator(tc.project, "", clock.Real())
			matches := findByCode(v.validateFMT32(), codeFMT32)
			if len(matches) != tc.wantCount {
				t.Fatalf("FMT-32 %s: expected %d findings, got %d: %v", tc.name, tc.wantCount, len(matches), matches)
			}
			if tc.wantCount == 1 {
				if matches[0].IssueType != tc.wantIssue {
					t.Errorf("FMT-32: expected IssueType=%v, got %v", tc.wantIssue, matches[0].IssueType)
				}
				if matches[0].Field != tc.wantField {
					t.Errorf("FMT-32: expected Field=%q, got %q", tc.wantField, matches[0].Field)
				}
				if matches[0].Severity != SeverityError {
					t.Errorf("FMT-32: expected SeverityError, got %v", matches[0].Severity)
				}
			}
		})
	}
}

// TestFMT32_MultipleContracts_Aggregate verifies that FMT-32 reports findings
// for all offending contracts in one pass (not short-circuit on first).
func TestFMT32_MultipleContracts_Aggregate(t *testing.T) {
	// Two serviceOwned contracts: first lacks ownership, second is valid.
	p := fmt32Project(true, nil, map[string]metadata.ParamSchema{"id": {Type: "string"}})
	p.Contracts["http.resource.other.v1"] = &metadata.ContractMeta{
		ID:               "http.resource.other.v1",
		Kind:             "http",
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server:  "samplecore",
			Clients: []string{"edgecell"},
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "POST",
				Path:          "/api/v1/resource",
				SuccessStatus: 201,
				Auth:          metadata.HTTPAuthMeta{ServiceOwned: true},
				Ownership: &metadata.HTTPOwnershipMeta{
					SubjectPath:  "ctx.userID",
					ResourcePath: "ctx.tenantID",
				},
			},
		},
		Dir:  "contracts/http/resource/other/v1",
		File: "contracts/http/resource/other/v1/contract.yaml",
	}
	v := NewValidator(p, "", clock.Real())
	matches := findByCode(v.validateFMT32(), codeFMT32)
	// Only the first contract should produce a finding (missing ownership block).
	if len(matches) != 1 {
		t.Fatalf("FMT-32 aggregate: expected 1 finding (first contract missing block), got %d: %v", len(matches), matches)
	}
}
