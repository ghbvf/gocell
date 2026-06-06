package governance

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// TestFMT13_MissingEndpointsHTTP verifies that an HTTP contract without
// endpoints.http produces a FMT-13 SeverityError.
func TestFMT13_MissingEndpointsHTTP(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {
				ID:               metadatatest.CellIDAccessCore,
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
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
	if !strings.Contains(fmt13Errors[0].Fix, "method:") {
		t.Errorf("FMT-13: expected 'method:' YAML template in fix guidance, got: %s", fmt13Errors[0].Fix)
	}
}

// fmt40Project builds a minimal HTTP contract carrying the given header map for
// FMT-40 validation.
func fmt40Project(headers map[string]metadata.ParamSchema) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:               "http.auth.login.v1",
				Kind:             "http",
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.CellIDAccessCore,
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/access/sessions/login",
						Headers:       headers,
						SuccessStatus: 201,
					},
				},
				File: "contracts/http/auth/login/v1/contract.yaml",
			},
		},
	}
}

func fmt40Errors(t *testing.T, headers map[string]metadata.ParamSchema) []ValidationResult {
	t.Helper()
	v := NewValidator(fmt40Project(headers), "", clock.Real())
	var out []ValidationResult
	for _, r := range v.validateFMT40() {
		if r.Code == codeFMT40 {
			out = append(out, r)
		}
	}
	return out
}

// TestFMT40_ValidHeaderAccepted verifies a canonical typed header passes.
func TestFMT40_ValidHeaderAccepted(t *testing.T) {
	truthy := true
	got := fmt40Errors(t, map[string]metadata.ParamSchema{
		"X-Tenant-ID": {Type: "string", Format: "uuid", Required: &truthy},
	})
	if len(got) != 0 {
		t.Fatalf("FMT-40: valid header must not be flagged, got %d: %v", len(got), got)
	}
}

// TestFMT40_NoHeadersSkipped verifies a contract without headers is not flagged.
func TestFMT40_NoHeadersSkipped(t *testing.T) {
	if got := fmt40Errors(t, nil); len(got) != 0 {
		t.Fatalf("FMT-40: contract with no headers must not be flagged, got: %v", got)
	}
}

// TestFMT40_Violations exercises each rejection branch.
func TestFMT40_Violations(t *testing.T) {
	minLen := 1
	cases := []struct {
		name       string
		headers    map[string]metadata.ParamSchema
		wantIssue  IssueType
		wantSubstr string
	}{
		{
			name:       "invalid name with space",
			headers:    map[string]metadata.ParamSchema{"X Tenant": {Type: "string"}},
			wantIssue:  IssueInvalid,
			wantSubstr: "not a valid HTTP header token",
		},
		{
			name:       "missing type",
			headers:    map[string]metadata.ParamSchema{"X-Tenant-ID": {}},
			wantIssue:  IssueRequired,
			wantSubstr: "declares no type",
		},
		{
			name:       "unknown type",
			headers:    map[string]metadata.ParamSchema{"X-Tenant-ID": {Type: "uuid"}},
			wantIssue:  IssueInvalid,
			wantSubstr: "is not one of string/integer/number/boolean",
		},
		{
			name:       "unenforced minLength rejected",
			headers:    map[string]metadata.ParamSchema{"X-Tenant-ID": {Type: "string", MinLength: &minLen}},
			wantIssue:  IssueForbidden,
			wantSubstr: "not codegen-enforced",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fmt40Errors(t, tc.headers)
			found := false
			for _, r := range got {
				if r.IssueType == tc.wantIssue && strings.Contains(r.Message, tc.wantSubstr) {
					found = true
				}
			}
			if !found {
				t.Fatalf("FMT-40 %s: expected issue %v containing %q, got: %v", tc.name, tc.wantIssue, tc.wantSubstr, got)
			}
		})
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
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   metadatatest.CellIDAccessCore,
					Subscribers: []string{metadatatest.CellIDAuditCore},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.NewCellID("somecore"),
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.NewCellID("somecore"),
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDEdgeBFF},
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
					Server:  metadatatest.CellIDSampleCore,
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
				Cells: []metadata.AssemblyCellRef{},
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
		Server:  metadatatest.CellIDSampleCore,
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
			Server: metadatatest.CellIDSampleCore,
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
					Server:  metadatatest.CellIDSampleCore,
					Clients: []string{metadatatest.NewCellID("edgecell")},
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
		{
			// path.id single-segment: the path param value itself is the owner key
			// (e.g. DELETE /users/{id} where {id} directly identifies the owned
			// resource). The dotIdx<0 branch treats the entire rest string as the
			// param name. pathParams contains "id" so referential integrity passes.
			name: "resourcePath path.id single-segment valid",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "ctx.userID",
				ResourcePath: "path.id",
			}, sidParam),
			wantCount: 0,
		},
		{
			// path.missing single-segment: dotIdx<0 branch extracts "missing" as
			// param name; pathParams does not contain "missing" so referential
			// integrity fails with IssueInvalid.
			name: "resourcePath path.missing single-segment not in pathParams",
			project: fmt32Project(true, &metadata.HTTPOwnershipMeta{
				SubjectPath:  "ctx.userID",
				ResourcePath: "path.missing",
			}, sidParam),
			wantCount: 1,
			wantIssue: IssueInvalid,
			wantField: "endpoints.http.ownership.resourcePath",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := NewValidator(tc.project, "", clock.Real())
			matches := findByCode(v.validateFMT32(), codeFMT32)
			assertFMT32Result(t, tc.name, matches, tc.wantCount, tc.wantIssue, tc.wantField)
		})
	}
}

// assertFMT32Result checks the FMT-32 finding count and, when exactly one
// finding is expected, validates its IssueType, Field, and Severity.
func assertFMT32Result(t *testing.T, caseName string, matches []ValidationResult, wantCount int, wantIssue IssueType, wantField string) {
	t.Helper()
	if len(matches) != wantCount {
		t.Fatalf("FMT-32 %s: expected %d findings, got %d: %v", caseName, wantCount, len(matches), matches)
	}
	if wantCount != 1 {
		return
	}
	if matches[0].IssueType != wantIssue {
		t.Errorf("FMT-32: expected IssueType=%v, got %v", wantIssue, matches[0].IssueType)
	}
	if matches[0].Field != wantField {
		t.Errorf("FMT-32: expected Field=%q, got %q", wantField, matches[0].Field)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-32: expected SeverityError, got %v", matches[0].Severity)
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
			Server:  metadatatest.CellIDSampleCore,
			Clients: []string{metadatatest.NewCellID("edgecell")},
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

// fmt33Usage is one (contract, kind, http-path, role) tuple attached to the
// single slice that fmt33Project builds. dangling=true declares the usage
// but skips registering the contract (exercises the REF-05-covered missing
// contract branch).
type fmt33Usage struct {
	id, kind, path, role string
	dangling             bool
}

// fmt33Project builds a *metadata.ProjectMeta with exactly one slice whose
// contractUsages mirror the supplied tuples; each referenced contract is
// auto-registered. Only the fields validateFMT33 reads are populated.
func fmt33Project(usages []fmt33Usage) *metadata.ProjectMeta {
	p := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	var cus []metadata.ContractUsage
	for _, u := range usages {
		cus = append(cus, metadata.ContractUsage{Contract: u.id, Role: u.role})
		if u.dangling {
			continue // declared usage, contract intentionally unregistered
		}
		cm := &metadata.ContractMeta{
			ID:               u.id,
			Kind:             u.kind,
			ConsistencyLevel: "L1",
			Lifecycle:        "active",
			File:             "contracts/" + u.id + "/contract.yaml",
		}
		if u.kind == "http" && u.path != "" {
			cm.Endpoints = metadata.EndpointsMeta{
				Server: metadatatest.CellIDSampleCore,
				HTTP: &metadata.HTTPTransportMeta{
					Method: "GET", Path: u.path, SuccessStatus: 200,
				},
			}
		}
		p.Contracts[u.id] = cm
	}
	p.Slices["samplecore/sampleslice"] = &metadata.SliceMeta{
		ID:             "sampleslice",
		BelongsToCell:  metadatatest.CellIDSampleCore,
		ContractUsages: cus,
		File:           "cells/samplecore/slices/sampleslice/slice.yaml",
	}
	return p
}

// TestFMT33_VisibilitySegregation pins SLICE-HTTP-VISIBILITY-SEGREGATION-01.
// Case "mixed_public_internal_error" is the configread regression: the
// pre-split slice serving http.config.get.v1 + http.config.internal.get.v1
// must flag exactly one SeverityError; the two post-split cases prove the
// remediation goes green.
func TestFMT33_VisibilitySegregation(t *testing.T) {
	tests := []struct {
		name      string
		usages    []fmt33Usage
		wantCount int
	}{
		{
			// post-split public slice: get + list, same trust boundary.
			name: "public_only_ok",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"http.config.list.v1", "http", "/api/v1/config/", "serve", false},
			},
			wantCount: 0,
		},
		{
			// post-split internal slice: internal-get alone.
			name: "internal_only_ok",
			usages: []fmt33Usage{
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "serve", false},
			},
			wantCount: 0,
		},
		{
			// pre-split configread: public + internal in one slice → violation.
			name: "mixed_public_internal_error",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"http.config.list.v1", "http", "/api/v1/config/", "serve", false},
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "serve", false},
			},
			wantCount: 1,
		},
		{
			// internal contract used as a client (role=call) is unrelated.
			name: "internal_call_role_skipped",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "call", false},
			},
			wantCount: 0,
		},
		{
			// non-http kind never participates even if a path string is set.
			name: "event_kind_skipped",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"event.config.entry-upserted.v1", "event", "", "subscribe", false},
			},
			wantCount: 0,
		},
		{
			// paths that are neither /api/v1 nor /internal/v1 (bootstrap) set
			// no flag — reverse self-check against over-firing.
			name: "neither_prefix_no_false_positive",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"http.health.v1", "http", "/healthz", "serve", false},
			},
			wantCount: 0,
		},
		{
			// dangling contract ref (unregistered) is skipped — REF-05's
			// domain — so it cannot pair with the public usage into a
			// false violation.
			name: "dangling_internal_ref_skipped",
			usages: []fmt33Usage{
				{"http.config.get.v1", "http", "/api/v1/config/{key}", "serve", false},
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "serve", true},
			},
			wantCount: 0,
		},
		{
			// http-kind contract with nil endpoints.http is skipped
			// (FMT-07/FMT-13's domain) so it cannot pair into a violation.
			name: "http_kind_nil_endpoint_skipped",
			usages: []fmt33Usage{
				{"http.broken.v1", "http", "", "serve", false},
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "serve", false},
			},
			wantCount: 0,
		},
		{
			// oracle generalisation: /api/v2 is public-surface, mixing with
			// /internal/v1 in the same slice must trigger — locks the F-A fix
			// that replaced the hard-coded /api/v1 prefix with IsPublicHTTPPath.
			name: "api_v2_with_internal_error",
			usages: []fmt33Usage{
				{"http.x.v2", "http", "/api/v2/x", "serve", false},
				{"http.y.internal.v1", "http", "/internal/v1/y", "serve", false},
			},
			wantCount: 1,
		},
		{
			// reverse self-check: two internal serve usages and no public one
			// must never fire — guards against hasPublic/hasInternal logic swap.
			name: "two_internal_no_public_ok",
			usages: []fmt33Usage{
				{"http.config.internal.get.v1", "http", "/internal/v1/config/{key}", "serve", false},
				{"http.config.internal.list.v1", "http", "/internal/v1/config/", "serve", false},
			},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := fmt33Project(tc.usages)
			v := NewValidator(p, "", clock.Real())
			matches := findByCode(v.validateFMT33(), codeFMT33)
			if len(matches) != tc.wantCount {
				t.Fatalf("FMT-33 %s: expected %d findings, got %d: %v",
					tc.name, tc.wantCount, len(matches), matches)
			}
			if tc.wantCount == 1 {
				if matches[0].Severity != SeverityError {
					t.Errorf("FMT-33: expected SeverityError, got %v", matches[0].Severity)
				}
				if matches[0].Field != "contractUsages" {
					t.Errorf("FMT-33: expected Field=contractUsages, got %q", matches[0].Field)
				}
			}
		})
	}
}

// TestFMT34_PublicBypassOnInternalPath verifies that FMT-34 forbids
// auth.public / auth.passwordResetExempt on /internal/v1/* paths while
// remaining orthogonal to the legitimate internal-path auth shapes
// (bootstrap, serviceOwned, clientsOnly).
//
// FMT-34 is the metadata-stage upstream of runtime/auth/route.go
// validateBypassCompatibility, which catches the same shape at runtime.
// FMT-26 catches the two-bypass mutex orthogonally (auth.public vs
// auth.passwordResetExempt mutual exclusion) regardless of path.
// fmt34TestCase carries inputs and expectations for a single FMT-34
// scenario. Defined at file scope so the runFMT34Case / assertFMT34Findings
// helpers can accept it as a typed argument — keeping the table-driven test
// body small enough to satisfy the kernel/ cognitive complexity budget.
type fmt34TestCase struct {
	name       string
	path       string
	auth       metadata.HTTPAuthMeta
	clients    []string
	nilHTTP    bool
	wantCount  int
	wantFields []string
}

func TestFMT34_PublicBypassOnInternalPath(t *testing.T) {
	t.Parallel()

	tests := []fmt34TestCase{
		{
			name:       "public_on_internal_path",
			path:       "/internal/v1/foo",
			auth:       metadata.HTTPAuthMeta{Public: true},
			wantCount:  1,
			wantFields: []string{"endpoints.http.auth.public"},
		},
		{
			name:       "password_reset_exempt_on_internal_path",
			path:       "/internal/v1/foo",
			auth:       metadata.HTTPAuthMeta{PasswordResetExempt: true},
			wantCount:  1,
			wantFields: []string{"endpoints.http.auth.passwordResetExempt"},
		},
		{
			// FMT-34 emits one finding per bypass flag (parallel to FMT-28's
			// multi-finding shape); FMT-26 separately catches the mutex.
			name:      "both_flags_on_internal_path",
			path:      "/internal/v1/foo",
			auth:      metadata.HTTPAuthMeta{Public: true, PasswordResetExempt: true},
			wantCount: 2,
			wantFields: []string{
				"endpoints.http.auth.public",
				"endpoints.http.auth.passwordResetExempt",
			},
		},
		{
			// IsInternalHTTPPath oracle covers exact prefix /internal/v1
			// (no trailing slash) — locks the predicate boundary.
			name:       "exact_prefix_internal_v1",
			path:       "/internal/v1",
			auth:       metadata.HTTPAuthMeta{Public: true},
			wantCount:  1,
			wantFields: []string{"endpoints.http.auth.public"},
		},
		{
			// bootstrap on /internal/v1/* is NOT a legitimate shape — FMT-28
			// narrows bootstrap to /api/v{N}/{cell}/setup/admin only. FMT-34
			// itself does not inspect the Bootstrap flag, so wantCount=0 here
			// locks scope separation (FMT-34 only checks Public /
			// PasswordResetExempt). FMT-28 catches the path violation in a
			// separate rule.
			name:      "bootstrap_flag_not_fmt34_concern",
			path:      "/internal/v1/foo",
			auth:      metadata.HTTPAuthMeta{Bootstrap: true},
			wantCount: 0,
		},
		{
			// serviceOwned keeps listener JWT auth and delegates ownership
			// to the service — legitimate on internal paths.
			name:      "service_owned_on_internal_path_ok",
			path:      "/internal/v1/foo",
			auth:      metadata.HTTPAuthMeta{ServiceOwned: true},
			wantCount: 0,
		},
		{
			// clientsOnly is the standard caller-cell-allowlist shape on
			// internal paths.
			name:      "clients_only_on_internal_path_ok",
			path:      "/internal/v1/foo",
			auth:      metadata.HTTPAuthMeta{ClientsOnly: true},
			clients:   []string{"edge-bff"},
			wantCount: 0,
		},
		{
			// path predicate must converge: public on /api/v1 is the standard
			// public-endpoint shape and must never trigger FMT-34.
			name:      "public_on_public_path_ok",
			path:      "/api/v1/auth/login",
			auth:      metadata.HTTPAuthMeta{Public: true},
			wantCount: 0,
		},
		{
			// IsInternalHTTPPath is strictly /internal/v1 — future version paths
			// must not trigger FMT-34 (locks oracle against version drift).
			name:      "internal_v2_not_internal_path",
			path:      "/internal/v2/foo",
			auth:      metadata.HTTPAuthMeta{Public: true},
			wantCount: 0,
		},
		{
			// non-HTTP contract (e.g. event) short-circuits FMT-34.
			name:      "non_http_contract_skipped",
			nilHTTP:   true,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runFMT34Case(t, tt)
		})
	}
}

// runFMT34Case builds the project fixture for one FMT-34 scenario, runs
// validateFMT34, and delegates result validation to assertFMT34Findings.
// Extracted from TestFMT34_PublicBypassOnInternalPath to keep cognitive
// complexity under the kernel/ budget of 15.
func runFMT34Case(t *testing.T, tt fmt34TestCase) {
	t.Helper()
	contract := &metadata.ContractMeta{
		ID:               "http.fmt34." + tt.name + ".v1",
		Kind:             "http",
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server:  metadatatest.CellIDAccessCore,
			Clients: tt.clients,
		},
		Dir:  "contracts/http/fmt34/" + tt.name + "/v1",
		File: "contracts/http/fmt34/" + tt.name + "/v1/contract.yaml",
	}
	if !tt.nilHTTP {
		contract.Endpoints.HTTP = &metadata.HTTPTransportMeta{
			Method:        "GET",
			Path:          tt.path,
			SuccessStatus: 200,
			Auth:          tt.auth,
		}
	}

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{contract.ID: contract},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT34(), codeFMT34)
	assertFMT34Findings(t, tt, matches)
}

// assertFMT34Findings checks that matches has the expected count, that every
// finding carries SeverityError and a non-empty Fix field (INV-3 contract), and that
// every expected field path appears in the result set.
func assertFMT34Findings(t *testing.T, tt fmt34TestCase, matches []ValidationResult) {
	t.Helper()
	if len(matches) != tt.wantCount {
		t.Fatalf("FMT-34 %s: expected %d findings, got %d: %v",
			tt.name, tt.wantCount, len(matches), matches)
	}
	for _, m := range matches {
		assertFMT34Severity(t, tt.name, m)
		assertFMT34FixSuffix(t, tt.name, m)
	}
	gotFields := collectFMT34Fields(matches)
	for _, want := range tt.wantFields {
		if !gotFields[want] {
			t.Errorf("FMT-34 %s: expected finding on field %q, got fields %v",
				tt.name, want, gotFields)
		}
	}
}

func assertFMT34Severity(t *testing.T, name string, m ValidationResult) {
	t.Helper()
	if m.Severity != SeverityError {
		t.Errorf("FMT-34 %s: expected SeverityError, got %v", name, m.Severity)
	}
}

// assertFMT34FixSuffix checks that every FMT-34 finding has a non-empty Fix
// field — the INV-3 contract that fix guidance is always present on errors.
func assertFMT34FixSuffix(t *testing.T, name string, m ValidationResult) {
	t.Helper()
	if m.Fix == "" {
		t.Errorf("FMT-34 %s: Fix field must be non-empty, got empty fix for message: %q",
			name, m.Message)
	}
}

func collectFMT34Fields(matches []ValidationResult) map[string]bool {
	out := make(map[string]bool, len(matches))
	for _, m := range matches {
		out[m.Field] = true
	}
	return out
}

// --- FMT-35: contractUsage placement columns per role ---
//
// FMT-35 enforces which of the seven placement columns (handler, group, field,
// sourceID, targetSelector, projection, onReset) may or must be set for each
// contractUsage role. The matrix is role×kind sensitive: grpc serve allows the
// optional field column (struct-field disambiguation) while http serve forbids it.

// buildFMT35Project returns a ProjectMeta with one cell, one slice and a single
// contractUsage entry. contractKind selects which contract kind is created ("http"
// or "grpc"); cu is the usage to validate.
func buildFMT35Project(contractKind string, cu metadata.ContractUsage) *metadata.ProjectMeta {
	const contractID = "test.serve.v1"
	cu.Contract = contractID
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:    metadatatest.CellIDTestCell,
				Owner: metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Dir:   "testcell",
				File:  "cells/testcell/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"testcell/testslice": {
				ID:             "testslice",
				BelongsToCell:  metadatatest.CellIDTestCell,
				ContractUsages: []metadata.ContractUsage{cu},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.testslice.service"},
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/testcell/slices/testslice/**"},
				Dir:          "testslice",
				CellDir:      "testcell",
				File:         "cells/testcell/slices/testslice/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			contractID: {
				ID:               contractID,
				Kind:             contractKind,
				OwnerCell:        metadatatest.CellIDTestCell,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDTestCell},
				Dir:              "contracts/" + contractKind + "/test/serve/v1",
				File:             "contracts/" + contractKind + "/test/serve/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestFMT35_GRPCServe_FieldOptional_NoFinding verifies that a grpc serve CU
// with field set produces 0 FMT-35 findings (field is optional for grpc serve).
func TestFMT35_GRPCServe_FieldOptional_NoFinding(t *testing.T) {
	cu := metadata.ContractUsage{Role: "serve", Field: "myGrpcSvc"}
	project := buildFMT35Project("grpc", cu)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT35(), codeFMT35)
	if len(matches) != 0 {
		t.Fatalf("FMT-35: grpc serve with field set must produce 0 findings, got %d: %v", len(matches), matches)
	}
}

// TestFMT35_GRPCServe_NoOptionalColumns_NoFinding verifies that a grpc serve CU
// with no optional columns set also produces 0 FMT-35 findings.
func TestFMT35_GRPCServe_NoOptionalColumns_NoFinding(t *testing.T) {
	cu := metadata.ContractUsage{Role: "serve"}
	project := buildFMT35Project("grpc", cu)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT35(), codeFMT35)
	if len(matches) != 0 {
		t.Fatalf("FMT-35: grpc serve with no optional columns must produce 0 findings, got %d: %v", len(matches), matches)
	}
}

// TestFMT35_GRPCServe_HandlerForbidden verifies that a grpc serve CU with
// handler set produces a FMT-35 finding (handler is forbidden for grpc serve).
func TestFMT35_GRPCServe_HandlerForbidden(t *testing.T) {
	cu := metadata.ContractUsage{Role: "serve", Handler: "HandleRPC"}
	project := buildFMT35Project("grpc", cu)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT35(), codeFMT35)
	if len(matches) != 1 {
		t.Fatalf("FMT-35: grpc serve with handler set must produce 1 finding, got %d: %v", len(matches), matches)
	}
	if matches[0].Field != "contractUsages[0].handler" {
		t.Errorf("FMT-35: expected Field=contractUsages[0].handler, got %q", matches[0].Field)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-35: expected SeverityError, got %v", matches[0].Severity)
	}
	if matches[0].Fix == "" {
		t.Errorf("FMT-35: Fix field must be non-empty")
	}
}

// TestFMT35_HTTPServe_FieldForbidden is a regression guard: a http serve CU
// with field set must still produce a FMT-35 finding (http serve forbids field).
func TestFMT35_HTTPServe_FieldForbidden(t *testing.T) {
	cu := metadata.ContractUsage{Role: "serve", Field: "someField"}
	project := buildFMT35Project("http", cu)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT35(), codeFMT35)
	if len(matches) != 1 {
		t.Fatalf("FMT-35: http serve with field set must produce 1 finding, got %d: %v", len(matches), matches)
	}
	if matches[0].Field != "contractUsages[0].field" {
		t.Errorf("FMT-35: expected Field=contractUsages[0].field, got %q", matches[0].Field)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-35: expected SeverityError, got %v", matches[0].Severity)
	}
	if matches[0].Fix == "" {
		t.Errorf("FMT-35: Fix field must be non-empty")
	}
}

// --- FMT-36: cell.requires must be known capability enum values, no duplicates ---
//
// Design Y (#855): capability dependency is declared per-cell via cell.yaml
// `requires`; the assembly's provisioned capability set is the derived union.
// FMT-36 validates the per-cell declaration (well-formedness), not an
// assembly-level list.

// buildFMT36Project returns a minimal ProjectMeta with one cell whose
// requires field is set to the given slice.
func buildFMT36Project(requires []string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:       metadatatest.CellIDTestCell,
				Requires: requires,
				Owner:    metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Dir:      "testcell",
				File:     "cells/testcell/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestFMT36_ValidRequires verifies that a known subset produces 0 findings.
func TestFMT36_ValidRequires(t *testing.T) {
	project := buildFMT36Project([]string{"postgres", "redis"})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 0 {
		t.Fatalf("FMT-36: expected 0 findings for valid requires, got: %v", matches)
	}
}

// TestFMT36_EmptyRequires verifies that an empty requires list is accepted.
func TestFMT36_EmptyRequires(t *testing.T) {
	project := buildFMT36Project([]string{})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 0 {
		t.Fatalf("FMT-36: expected 0 findings for empty requires, got: %v", matches)
	}
}

// TestFMT36_NilRequires verifies that a nil requires field is accepted.
func TestFMT36_NilRequires(t *testing.T) {
	project := buildFMT36Project(nil)
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 0 {
		t.Fatalf("FMT-36: expected 0 findings for nil requires, got: %v", matches)
	}
}

// TestFMT36_AllThree verifies that all three enum members together pass.
func TestFMT36_AllThree(t *testing.T) {
	project := buildFMT36Project([]string{"postgres", "redis", "rabbitmq"})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 0 {
		t.Fatalf("FMT-36: expected 0 findings for all three requires, got: %v", matches)
	}
}

// TestFMT36_UnknownRequires verifies that a single unknown value produces
// exactly 1 error finding on field "requires[0]".
func TestFMT36_UnknownRequires(t *testing.T) {
	project := buildFMT36Project([]string{"foobar"})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 1 {
		t.Fatalf("FMT-36: expected 1 finding for unknown requires, got %d: %v", len(matches), matches)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-36: expected SeverityError, got %v", matches[0].Severity)
	}
	if matches[0].Field != "requires[0]" {
		t.Errorf("FMT-36: expected Field=requires[0], got %q", matches[0].Field)
	}
	if matches[0].Fix == "" {
		t.Errorf("FMT-36: Fix field must be non-empty")
	}
}

// TestFMT36_DuplicateRequires verifies that a duplicate value produces
// exactly 1 error finding on field "requires[1]".
func TestFMT36_DuplicateRequires(t *testing.T) {
	project := buildFMT36Project([]string{"postgres", "postgres"})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 1 {
		t.Fatalf("FMT-36: expected 1 finding for duplicate requires, got %d: %v", len(matches), matches)
	}
	if matches[0].Severity != SeverityError {
		t.Errorf("FMT-36: expected SeverityError, got %v", matches[0].Severity)
	}
	if matches[0].Field != "requires[1]" {
		t.Errorf("FMT-36: expected Field=requires[1], got %q", matches[0].Field)
	}
	if matches[0].Fix == "" {
		t.Errorf("FMT-36: Fix field must be non-empty")
	}
}

// TestFMT36_MultipleErrors verifies that unknown + duplicate each produce a finding.
func TestFMT36_MultipleErrors(t *testing.T) {
	// "redis" valid, "foobar" unknown, "redis" duplicate — two findings expected.
	project := buildFMT36Project([]string{"redis", "foobar", "redis"})
	v := NewValidator(project, "", clock.Real())
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 2 {
		t.Fatalf("FMT-36: expected 2 findings (unknown + duplicate), got %d: %v", len(matches), matches)
	}
	for _, m := range matches {
		if m.Severity != SeverityError {
			t.Errorf("FMT-36: all findings must be SeverityError, got %v", m.Severity)
		}
		if m.Fix == "" {
			t.Errorf("FMT-36: Fix field must be non-empty for message %q", m.Message)
		}
	}
}

// TestFMT36_NilCell verifies that a nil cell entry does not panic.
func TestFMT36_NilCell(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("nilcell"): nil,
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, "", clock.Real())
	// Must not panic; nil cell guard mirrors validateFMT29/FMT30.
	matches := findByCode(v.validateFMT36(), codeFMT36)
	if len(matches) != 0 {
		t.Fatalf("FMT-36: expected 0 findings for nil cell, got: %v", matches)
	}
}

// ---- FMT-07 saga fan-out + FMT-09 kind list --------------------------------

// TestFMT07_SagaMissingServer verifies that a kind=saga contract with an empty
// endpoints.server triggers a FMT-07 error with Field == "endpoints.server".
// Task 3 requirement: the existing TestFMT07 in validate_test.go only asserts
// Len/Severity; this test adds the Field assertion for the saga arm.
func TestFMT07_SagaMissingServer(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"saga.order.checkout.v1": {
				ID:               "saga.order.checkout.v1",
				Kind:             "saga",
				ConsistencyLevel: "L3",
				Lifecycle:        "active",
				// Endpoints.Server deliberately empty → FMT-07 must fire.
				Endpoints: metadata.EndpointsMeta{},
				Dir:       "contracts/saga/order/checkout/v1",
				File:      "contracts/saga/order/checkout/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, "", clock.Real())
	got := findByCode(v.validateFMT07(), codeFMT07)
	if len(got) != 1 {
		t.Fatalf("FMT-07 saga: expected 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Field != "endpoints.server" {
		t.Errorf("FMT-07 saga: expected Field=endpoints.server, got %q", got[0].Field)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("FMT-07 saga: expected SeverityError, got %v", got[0].Severity)
	}
}

// TestFMT07_SagaWithServer verifies that a kind=saga contract with a populated
// endpoints.server does NOT trigger FMT-07.
func TestFMT07_SagaWithServer(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"saga.order.checkout.v1": {
				ID:               "saga.order.checkout.v1",
				Kind:             "saga",
				ConsistencyLevel: "L3",
				Lifecycle:        "active",
				Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDTestCell},
				Dir:              "contracts/saga/order/checkout/v1",
				File:             "contracts/saga/order/checkout/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, "", clock.Real())
	got := findByCode(v.validateFMT07(), codeFMT07)
	if len(got) != 0 {
		t.Fatalf("FMT-07 saga: expected 0 findings for populated server, got %d: %v", len(got), got)
	}
}

// TestFMT09_InvalidKindEnumeratesSaga verifies that FMT-09's error message for
// an out-of-set kind includes "saga" (derived from cellvocab.AllContractKinds).
func TestFMT09_InvalidKindEnumeratesSaga(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"websocket.test.v1": {
				ID:               "websocket.test.v1",
				Kind:             "websocket",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDTestCell},
				Dir:              "contracts/websocket/test/v1",
				File:             "contracts/websocket/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, "", clock.Real())
	got := findByCode(v.validateFMT09(), codeFMT09)
	if len(got) != 1 {
		t.Fatalf("FMT-09: expected 1 finding for invalid kind, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "saga") {
		t.Errorf("FMT-09: message should enumerate 'saga' (derived from AllContractKinds), got: %s", got[0].Message)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("FMT-09: expected SeverityError, got %v", got[0].Severity)
	}
}
