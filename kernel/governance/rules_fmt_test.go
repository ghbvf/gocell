package governance

import (
	"strings"
	"testing"

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

	v := NewValidator(project, "")
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

	v := NewValidator(project, "")
	results := v.validateFMT13()

	for _, r := range results {
		if r.Code == codeFMT13 {
			t.Errorf("FMT-13: unexpected finding on non-HTTP contract: %v", r)
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

	v := NewValidator(project, "")
	results := v.validateFMT13()

	for _, r := range results {
		if r.Code == codeFMT13 && r.Severity == SeverityError {
			t.Errorf("FMT-13: unexpected error for contract with valid endpoints.http: %v", r)
		}
	}
}
