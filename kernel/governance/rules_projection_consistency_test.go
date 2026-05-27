package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	metadatatest "github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

func boolPtr(b bool) *bool { return &b }

// buildProjectionProject creates a minimal ProjectMeta with a single
// projection contract for PROJECTION-CONSISTENCY-01 tests.
func buildProjectionProject(consistencyLevel string) *metadata.ProjectMeta {
	c := &metadata.ContractMeta{
		ID:               "projection.test.summary.v1",
		Kind:             "projection",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: consistencyLevel,
		Lifecycle:        "active",
		Replayable:       boolPtr(true),
		Endpoints: metadata.EndpointsMeta{
			Provider: metadatatest.CellIDTestCell,
			Readers:  []string{metadatatest.CellIDEdgeBFF},
		},
		Dir:  "contracts/projection/test/summary/v1",
		File: "contracts/projection/test/summary/v1/contract.yaml",
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:               metadatatest.CellIDTestCell,
				Type:             "core",
				ConsistencyLevel: "L3",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_test"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.testcell.startup"}},
				Dir:              "cells/testcell",
				File:             "cells/testcell/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{"projection.test.summary.v1": c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// buildNonProjectionProject creates a minimal ProjectMeta with a non-projection
// contract (http or event) at L0 to verify PROJECTION-CONSISTENCY-01 skips it.
func buildNonProjectionProject(kind, consistencyLevel string) *metadata.ProjectMeta {
	c := &metadata.ContractMeta{
		ID:               "http.test.action.v1",
		Kind:             kind,
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: consistencyLevel,
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server:  metadatatest.CellIDTestCell,
			Clients: []string{metadatatest.CellIDEdgeBFF},
		},
		Dir:  "contracts/" + kind + "/test/action/v1",
		File: "contracts/" + kind + "/test/action/v1/contract.yaml",
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:               metadatatest.CellIDTestCell,
				Type:             "core",
				ConsistencyLevel: "L0",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_test"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.testcell.startup"}},
				Dir:              "cells/testcell",
				File:             "cells/testcell/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{c.ID: c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestProjectionConsistency01 is a table-driven test for
// validateProjectionConsistency (PROJECTION-CONSISTENCY-01).
//
// Rule: contracts with kind=projection must declare consistencyLevel >= L3.
// Non-projection contracts are not checked.
func TestProjectionConsistency01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		project      *metadata.ProjectMeta
		wantErrCount int
		wantField    string
	}{
		{
			name:         "projection L2 → error",
			project:      buildProjectionProject("L2"),
			wantErrCount: 1,
			wantField:    "consistencyLevel",
		},
		{
			name:         "projection L1 → error",
			project:      buildProjectionProject("L1"),
			wantErrCount: 1,
			wantField:    "consistencyLevel",
		},
		{
			name:         "projection L0 → error",
			project:      buildProjectionProject("L0"),
			wantErrCount: 1,
			wantField:    "consistencyLevel",
		},
		{
			name:         "projection empty → error (in-memory fixture bypass prevention)",
			project:      buildProjectionProject(""),
			wantErrCount: 1,
			wantField:    "consistencyLevel",
		},
		{
			name:         "projection L3 → no error",
			project:      buildProjectionProject("L3"),
			wantErrCount: 0,
		},
		{
			name:         "projection L4 → no error",
			project:      buildProjectionProject("L4"),
			wantErrCount: 0,
		},
		{
			name:         "http L0 → no error (non-projection skipped)",
			project:      buildNonProjectionProject("http", "L0"),
			wantErrCount: 0,
		},
		{
			name:         "event L0 → no error (non-projection skipped)",
			project:      buildNonProjectionProject("event", "L0"),
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator(tc.project, "", clock.Real())
			results := v.validateProjectionConsistency()

			// filter to PROJECTION-CONSISTENCY-01 results
			var got []ValidationResult
			for _, r := range results {
				if r.Code == codePROJECTIONCONSISTENCY01 {
					got = append(got, r)
				}
			}

			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s) with code %s, got %d: %v",
					tc.wantErrCount, codePROJECTIONCONSISTENCY01, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
			}
		})
	}
}
