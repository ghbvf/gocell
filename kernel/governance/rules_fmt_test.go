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

// TestFMT18_ContractClientsLiteralMatchesYAML verifies that FMT-18 produces
// a SeverityError when the Clients set declared in a wrapper.ContractSpec
// literal (Go source) disagrees with the endpoints.clients list in the
// authoritative contract.yaml. This is the RED anchor for Wave 2: the
// contractSpecLiteral.clients field and the corresponding YAML cross-check
// in validateContractSpecLiteral do not exist yet — this test will fail to
// compile until Wave 2 adds the field and the check.
func TestFMT18_ContractClientsLiteralMatchesYAML(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.role.assign.v1": {
				ID:               "http.auth.role.assign.v1",
				Kind:             "http",
				OwnerCell:        "accesscore",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"configcore"}, // YAML declares configcore
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/internal/v1/access/roles/assign",
						SuccessStatus: 200,
					},
				},
				Dir:  "contracts/http/auth/role/assign/v1",
				File: "contracts/http/auth/role/assign/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, t.TempDir(), clock.Real())

	// Go source declares "accesscore" as the only caller, but YAML says "configcore".
	// FMT-18 must detect the mismatch and surface a SeverityError.
	//
	// Wave-2 RED: contractSpecLiteral.clients field does not exist yet — this
	// literal will fail to compile until Wave 2 adds the field.
	lit := contractSpecLiteral{
		file:    "cells/accesscore/slices/rbacassign/routes.go",
		line:    12,
		id:      "http.auth.role.assign.v1",
		kind:    "http",
		method:  "POST",
		path:    "/internal/v1/access/roles/assign",
		clients: []string{"accesscore"}, // Go literal: accesscore — YAML says configcore
	}

	results := v.validateContractSpecLiteral(lit)

	var fmt18Errors []ValidationResult
	for _, r := range results {
		if r.Code == codeFMT18 && r.Severity == SeverityError {
			fmt18Errors = append(fmt18Errors, r)
		}
	}
	if len(fmt18Errors) == 0 {
		t.Fatal("FMT-18: expected SeverityError for clients mismatch between Go literal and YAML, got none")
	}
	found := false
	for _, r := range fmt18Errors {
		if strings.Contains(r.Message, "clients") || strings.Contains(r.Message, "Clients") {
			found = true
			if !strings.Contains(r.Message, "http.auth.role.assign.v1") {
				t.Errorf("FMT-18 clients error must name the contract ID, got: %s", r.Message)
			}
			break
		}
	}
	if !found {
		t.Errorf("FMT-18: expected error message to mention 'clients', got: %v", fmt18Errors)
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
