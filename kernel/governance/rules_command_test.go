// INVARIANT: COMMAND-CONTRACT-SCHEMA-REF-01

package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

const commandTestContractID = "command.test.device.enroll.v1"

// buildCommandProject builds a minimal *metadata.ProjectMeta containing exactly
// one command contract with the given schemaRefs.
func buildCommandProject(request, response string) *metadata.ProjectMeta {
	c := &metadata.ContractMeta{
		ID:               commandTestContractID,
		Kind:             "command",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDTestCell,
		},
		SchemaRefs: metadata.SchemaRefsMeta{
			Request:  request,
			Response: response,
		},
		Dir:  "contracts/command/test/device/enroll/v1",
		File: "contracts/command/test/device/enroll/v1/contract.yaml",
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:               metadatatest.CellIDTestCell,
				Type:             "core",
				ConsistencyLevel: "L1",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_test"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.testcell.startup"}},
				Dir:              "cells/testcell",
				File:             "cells/testcell/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{commandTestContractID: c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_MissingRequest checks that a command contract
// with an empty schemaRefs.request emits a finding.
func TestCOMMANDCONTRACTSCHEMAREF01_MissingRequest(t *testing.T) {
	t.Parallel()
	project := buildCommandProject("", "response.schema.json")
	v := NewValidator(project, "", clock.Real())
	results := filterByCode(v.validateCOMMANDCONTRACTSCHEMAREF01(), codeCOMMANDCONTRACTSCHEMAREF01)

	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(results), results)
	}
	r := results[0]
	if r.Severity != SeverityError {
		t.Errorf("expected SeverityError, got %s", r.Severity)
	}
	if r.Field != "schemaRefs.request" {
		t.Errorf("expected field %q, got %q", "schemaRefs.request", r.Field)
	}
	if r.Fix == "" {
		t.Error("finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
	if r.Code != codeCOMMANDCONTRACTSCHEMAREF01 {
		t.Errorf("expected code %s, got %s", codeCOMMANDCONTRACTSCHEMAREF01, r.Code)
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_MissingResponse checks that a command contract
// with an empty schemaRefs.response emits a finding.
func TestCOMMANDCONTRACTSCHEMAREF01_MissingResponse(t *testing.T) {
	t.Parallel()
	project := buildCommandProject("request.schema.json", "")
	v := NewValidator(project, "", clock.Real())
	results := filterByCode(v.validateCOMMANDCONTRACTSCHEMAREF01(), codeCOMMANDCONTRACTSCHEMAREF01)

	if len(results) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(results), results)
	}
	r := results[0]
	if r.Severity != SeverityError {
		t.Errorf("expected SeverityError, got %s", r.Severity)
	}
	if r.Field != "schemaRefs.response" {
		t.Errorf("expected field %q, got %q", "schemaRefs.response", r.Field)
	}
	if r.Fix == "" {
		t.Error("finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_MissingBoth checks that a command contract
// with both refs missing emits two findings (one per missing ref).
func TestCOMMANDCONTRACTSCHEMAREF01_MissingBoth(t *testing.T) {
	t.Parallel()
	project := buildCommandProject("", "")
	v := NewValidator(project, "", clock.Real())
	results := filterByCode(v.validateCOMMANDCONTRACTSCHEMAREF01(), codeCOMMANDCONTRACTSCHEMAREF01)

	if len(results) != 2 {
		t.Fatalf("expected 2 findings (one per missing ref), got %d: %v", len(results), results)
	}
	for i, r := range results {
		if r.Severity != SeverityError {
			t.Errorf("results[%d]: expected SeverityError, got %s", i, r.Severity)
		}
		if r.Fix == "" {
			t.Errorf("results[%d]: finding must carry non-empty Fix guidance", i)
		}
	}
	// First finding should be for request, second for response (iteration order).
	if results[0].Field != "schemaRefs.request" {
		t.Errorf("results[0]: expected field %q, got %q", "schemaRefs.request", results[0].Field)
	}
	if results[1].Field != "schemaRefs.response" {
		t.Errorf("results[1]: expected field %q, got %q", "schemaRefs.response", results[1].Field)
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_BothPresent checks that a well-formed command
// contract with both schemaRefs set emits no findings.
func TestCOMMANDCONTRACTSCHEMAREF01_BothPresent(t *testing.T) {
	t.Parallel()
	project := buildCommandProject("request.schema.json", "response.schema.json")
	v := NewValidator(project, "", clock.Real())
	results := v.validateCOMMANDCONTRACTSCHEMAREF01()

	if len(results) != 0 {
		t.Fatalf("expected no findings for well-formed command contract, got %d: %v", len(results), results)
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_WhitespaceOnly checks that a schemaRef
// consisting only of whitespace is treated as empty and emits a finding.
func TestCOMMANDCONTRACTSCHEMAREF01_WhitespaceOnly(t *testing.T) {
	t.Parallel()
	project := buildCommandProject("   ", "response.schema.json")
	v := NewValidator(project, "", clock.Real())
	results := filterByCode(v.validateCOMMANDCONTRACTSCHEMAREF01(), codeCOMMANDCONTRACTSCHEMAREF01)

	if len(results) != 1 {
		t.Fatalf("expected 1 finding for whitespace-only request ref, got %d: %v", len(results), results)
	}
	if results[0].Field != "schemaRefs.request" {
		t.Errorf("expected field %q, got %q", "schemaRefs.request", results[0].Field)
	}
}

// TestCOMMANDCONTRACTSCHEMAREF01_NonCommandNotFlagged checks that a non-command
// contract with empty schemaRefs is NOT flagged by this rule. The rule is
// command-kind specific.
func TestCOMMANDCONTRACTSCHEMAREF01_NonCommandNotFlagged(t *testing.T) {
	t.Parallel()
	c := &metadata.ContractMeta{
		ID:         "http.test.users.v1",
		Kind:       "http",
		OwnerCell:  metadatatest.CellIDTestCell,
		Lifecycle:  "active",
		SchemaRefs: metadata.SchemaRefsMeta{}, // both empty
		Dir:        "contracts/http/test/users/v1",
		File:       "contracts/http/test/users/v1/contract.yaml",
	}
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID: metadatatest.CellIDTestCell,
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{"http.test.users.v1": c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateCOMMANDCONTRACTSCHEMAREF01()
	if len(results) != 0 {
		t.Fatalf("non-command contract must not be flagged by COMMAND-CONTRACT-SCHEMA-REF-01, got %d: %v", len(results), results)
	}
}
