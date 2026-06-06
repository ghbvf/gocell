// INVARIANT: COMMAND-CONTRACT-SCHEMA-REF-01
// INVARIANT: COMMAND-CONTRACT-CONSISTENCY-LEVEL-01

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

// --- COMMAND-CONTRACT-CONSISTENCY-LEVEL-01 (#1668) ---

// buildCommandProjectLevel reuses buildCommandProject (valid schemaRefs so it is
// not flagged by SCHEMA-REF-01) and overrides the command contract's declared
// consistencyLevel.
func buildCommandProjectLevel(level string) *metadata.ProjectMeta {
	p := buildCommandProject("request.schema.json", "response.schema.json")
	p.Contracts[commandTestContractID].ConsistencyLevel = level
	return p
}

// TestCOMMANDCONTRACTCONSISTENCYLEVEL01_L0Rejected checks that a command contract
// declaring consistencyLevel=L0 (LocalOnly) emits exactly one finding: a command
// crosses the local boundary and requires at least L1 (LocalTx) atomicity.
func TestCOMMANDCONTRACTCONSISTENCYLEVEL01_L0Rejected(t *testing.T) {
	t.Parallel()
	project := buildCommandProjectLevel("L0")
	v := NewValidator(project, "", clock.Real())
	results := filterByCode(v.validateCOMMANDCONTRACTCONSISTENCYLEVEL01(), codeCOMMANDCONTRACTCONSISTENCYLEVEL01)

	if len(results) != 1 {
		t.Fatalf("expected 1 finding for L0 command, got %d: %v", len(results), results)
	}
	r := results[0]
	if r.Severity != SeverityError {
		t.Errorf("expected SeverityError, got %s", r.Severity)
	}
	if r.Field != "consistencyLevel" {
		t.Errorf("expected field %q, got %q", "consistencyLevel", r.Field)
	}
	if r.Fix == "" {
		t.Error("finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
	if r.Code != codeCOMMANDCONTRACTCONSISTENCYLEVEL01 {
		t.Errorf("expected code %s, got %s", codeCOMMANDCONTRACTCONSISTENCYLEVEL01, r.Code)
	}
}

// TestCOMMANDCONTRACTCONSISTENCYLEVEL01_L1ToL4Accepted checks that command
// contracts declaring any level >= L1 (including L4 devicecommand) are not
// flagged — the rule is a lower-bound (>= L1), not an exact-level lock.
func TestCOMMANDCONTRACTCONSISTENCYLEVEL01_L1ToL4Accepted(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"L1", "L2", "L3", "L4"} {
		level := level
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			project := buildCommandProjectLevel(level)
			v := NewValidator(project, "", clock.Real())
			results := filterByCode(v.validateCOMMANDCONTRACTCONSISTENCYLEVEL01(), codeCOMMANDCONTRACTCONSISTENCYLEVEL01)
			if len(results) != 0 {
				t.Fatalf("level %s command must not be flagged, got %d: %v", level, len(results), results)
			}
		})
	}
}

// TestCOMMANDCONTRACTCONSISTENCYLEVEL01_EmptyOrInvalidNotDoubleReported checks
// that empty / unparseable consistencyLevel is NOT reported by this rule — it is
// bottomed out by FMT-03 (contract consistencyLevel validity) and the parser's
// non-empty rejection. Re-reporting it here would double-report the same root
// cause.
func TestCOMMANDCONTRACTCONSISTENCYLEVEL01_EmptyOrInvalidNotDoubleReported(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"", "L9", "garbage"} {
		level := level
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			project := buildCommandProjectLevel(level)
			v := NewValidator(project, "", clock.Real())
			results := filterByCode(v.validateCOMMANDCONTRACTCONSISTENCYLEVEL01(), codeCOMMANDCONTRACTCONSISTENCYLEVEL01)
			if len(results) != 0 {
				t.Fatalf("level %q must not be reported by CONSISTENCY-LEVEL-01 (FMT-03 backstop), got %d: %v", level, len(results), results)
			}
		})
	}
}

// TestCOMMANDCONTRACTCONSISTENCYLEVEL01_NonCommandNotFlagged checks that a
// non-command contract declaring L0 is NOT flagged by this rule. The rule is
// command-kind specific.
func TestCOMMANDCONTRACTCONSISTENCYLEVEL01_NonCommandNotFlagged(t *testing.T) {
	t.Parallel()
	c := &metadata.ContractMeta{
		ID:               "http.test.users.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: "L0",
		Lifecycle:        "active",
		Dir:              "contracts/http/test/users/v1",
		File:             "contracts/http/test/users/v1/contract.yaml",
	}
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {ID: metadatatest.CellIDTestCell},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{"http.test.users.v1": c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateCOMMANDCONTRACTCONSISTENCYLEVEL01()
	if len(results) != 0 {
		t.Fatalf("non-command contract must not be flagged by COMMAND-CONTRACT-CONSISTENCY-LEVEL-01, got %d: %v", len(results), results)
	}
}
