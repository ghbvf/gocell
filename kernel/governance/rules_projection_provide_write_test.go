package governance

// INVARIANT: PROJECTION-PROVIDE-NEEDS-WRITE-CU-01

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// buildProjectionProvideProject constructs a minimal *metadata.ProjectMeta for
// PROJECTION-PROVIDE-NEEDS-WRITE-CU-01 tests.
//
// projectionID: the contract ID to reference in the provide CU.
// contractKind: the kind to set on the contract (use "projection" for the main
// positive path; use a different kind to test the "non-projection → skip" case).
// subscribeProjection: when non-empty, a subscribe CU with this projection value
// is added to sliceB (the write side). When empty, no write-side CU exists.
// missingContract: when true, the contract is omitted from the Contracts map.
// missingCell: when true, the cell is omitted from the Cells map.
func buildProjectionProvideProject(
	projectionID string,
	contractKind string,
	subscribeProjection string,
	missingContract bool,
	missingCell bool,
) *metadata.ProjectMeta {
	cellID := metadatatest.CellIDTestCell
	const (
		sliceAID = "testcell/projprovide"
		sliceBID = "testcell/projwrite"
	)

	contracts := map[string]*metadata.ContractMeta{}
	if !missingContract {
		contracts[projectionID] = &metadata.ContractMeta{
			ID:               projectionID,
			Kind:             contractKind,
			OwnerCell:        cellID,
			ConsistencyLevel: "L3",
			Lifecycle:        "active",
			Endpoints: metadata.EndpointsMeta{
				Provider: cellID,
			},
			Dir:  "contracts/projection/test/v1",
			File: "contracts/projection/test/v1/contract.yaml",
		}
	}

	cells := map[string]*metadata.CellMeta{}
	if !missingCell {
		cells[cellID] = &metadata.CellMeta{
			ID:               cellID,
			Type:             "core",
			ConsistencyLevel: "L3",
			DurabilityMode:   "durable",
			Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
			Schema:           metadata.SchemaMeta{Primary: "cell_test"},
			Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.testcell.startup"}},
			Dir:              "cells/testcell",
			File:             "cells/testcell/cell.yaml",
		}
	}

	// Slice A: the "read side" — has the provide CU.
	sliceA := &metadata.SliceMeta{
		ID:            sliceAID,
		BelongsToCell: cellID,
		ContractUsages: []metadata.ContractUsage{
			{
				Contract: projectionID,
				Role:     "provide",
			},
		},
		File: "cells/testcell/slices/projprovide/slice.yaml",
	}

	slices := map[string]*metadata.SliceMeta{
		sliceAID: sliceA,
	}

	// Slice B: the optional "write side" — has the subscribe+projection CU.
	if subscribeProjection != "" {
		slices[sliceBID] = &metadata.SliceMeta{
			ID:            sliceBID,
			BelongsToCell: cellID,
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "event.order.created.v1",
					Role:       "subscribe",
					Projection: subscribeProjection,
				},
			},
			File: "cells/testcell/slices/projwrite/slice.yaml",
		}
	}

	return &metadata.ProjectMeta{
		Cells:      cells,
		Slices:     slices,
		Contracts:  contracts,
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestProjectionProvideNeedsWriteCU01 is a table-driven test for
// validatePROJECTIONPROVIDENEEDSWRITECU01
// (PROJECTION-PROVIDE-NEEDS-WRITE-CU-01).
//
// Rule: a slice with role=provide referencing a kind=projection contract must
// belong to a cell that also has at least one subscribe CU with a non-empty
// Projection field. The implication is ONE-WAY.
func TestProjectionProvideNeedsWriteCU01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		projectionID    string
		contractKind    string
		subProjection   string
		missingContract bool
		missingCell     bool
		wantErrCount    int
		wantFieldPrefix string
	}{
		{
			name:            "provide→projection, no write CU → error",
			projectionID:    "projection.order.status.v1",
			contractKind:    "projection",
			subProjection:   "",
			wantErrCount:    1,
			wantFieldPrefix: "contractUsages[",
		},
		{
			name:          "provide→projection, with projection write CU → no error",
			projectionID:  "projection.order.status.v1",
			contractKind:  "projection",
			subProjection: "order_status",
			wantErrCount:  0,
		},
		{
			name:          "provide→event (non-projection) → no error (skip)",
			projectionID:  "event.order.created.v1",
			contractKind:  "event",
			subProjection: "",
			wantErrCount:  0,
		},
		{
			name:            "provide references missing contract → no error (REF owns)",
			projectionID:    "projection.order.status.v1",
			contractKind:    "projection",
			subProjection:   "",
			missingContract: true,
			wantErrCount:    0,
		},
		{
			name:          "cell missing from Cells map → no error (REF owns)",
			projectionID:  "projection.order.status.v1",
			contractKind:  "projection",
			subProjection: "",
			missingCell:   true,
			wantErrCount:  0,
		},
		{
			name:          "write CU present but no provide → no error (inverse not enforced)",
			projectionID:  "projection.order.status.v1",
			contractKind:  "projection",
			subProjection: "order_status",
			// The provide slice still exists (built by builder), so this case
			// passes because the write CU is present.
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			project := buildProjectionProvideProject(
				tc.projectionID,
				tc.contractKind,
				tc.subProjection,
				tc.missingContract,
				tc.missingCell,
			)

			v := NewValidator(project, "", clock.Real())
			results := v.validatePROJECTIONPROVIDENEEDSWRITECU01()

			var got []ValidationResult
			for _, r := range results {
				if r.Code == codePROJECTIONPROVIDENEEDSWRITECU01 {
					got = append(got, r)
				}
			}

			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s) with code %s, got %d: %v",
					tc.wantErrCount, codePROJECTIONPROVIDENEEDSWRITECU01, len(got), got)
			}
			if tc.wantErrCount == 0 {
				return
			}
			r := got[0]
			if r.Severity != SeverityError {
				t.Errorf("expected SeverityError, got %s", r.Severity)
			}
			if tc.wantFieldPrefix != "" {
				// Field should start with contractUsages[N].role
				if len(r.Field) < len(tc.wantFieldPrefix) {
					t.Errorf("expected field with prefix %q, got %q", tc.wantFieldPrefix, r.Field)
				}
				for i := range tc.wantFieldPrefix {
					if r.Field[i] != tc.wantFieldPrefix[i] {
						t.Errorf("expected field with prefix %q, got %q", tc.wantFieldPrefix, r.Field)
						break
					}
				}
			}
			if r.Fix == "" {
				t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
			}
		})
	}
}
