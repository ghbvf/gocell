package governance

// INVARIANT: PROJECTION-PROVIDE-NEEDS-WRITE-CU-01

import (
	"strings"
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
// missingProvide: when true, the provide slice (sliceA) is omitted entirely, so
// the cell has no read side — used to test the one-way implication (a write CU
// without a provide must NOT fire).
func buildProjectionProvideProject(
	projectionID string,
	contractKind string,
	subscribeProjection string,
	missingContract bool,
	missingCell bool,
	missingProvide bool,
) *metadata.ProjectMeta {
	// cellID is used only for map keys below; struct cell-id *field* positions
	// must reference metadatatest.CellIDTestCell directly (FIXTURE-CELLID-TYPED-BUILDER-01).
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
			OwnerCell:        metadatatest.CellIDTestCell,
			ConsistencyLevel: "L3",
			Lifecycle:        "active",
			Endpoints: metadata.EndpointsMeta{
				Provider: metadatatest.CellIDTestCell,
			},
			Dir:  "contracts/projection/test/v1",
			File: "contracts/projection/test/v1/contract.yaml",
		}
	}

	cells := map[string]*metadata.CellMeta{}
	if !missingCell {
		cells[cellID] = &metadata.CellMeta{
			ID:               metadatatest.CellIDTestCell,
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

	slices := map[string]*metadata.SliceMeta{}

	// Slice A: the "read side" — has the provide CU. Omitted when missingProvide.
	if !missingProvide {
		slices[sliceAID] = &metadata.SliceMeta{
			ID:            sliceAID,
			BelongsToCell: metadatatest.CellIDTestCell,
			ContractUsages: []metadata.ContractUsage{
				{
					Contract: projectionID,
					Role:     "provide",
				},
			},
			File: "cells/testcell/slices/projprovide/slice.yaml",
		}
	}

	// Slice B: the optional "write side" — has the subscribe+projection CU.
	if subscribeProjection != "" {
		slices[sliceBID] = &metadata.SliceMeta{
			ID:            sliceBID,
			BelongsToCell: metadatatest.CellIDTestCell,
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

type projectionProvideNeedsWriteCase struct {
	name            string
	projectionID    string
	contractKind    string
	subProjection   string
	missingContract bool
	missingCell     bool
	missingProvide  bool
	wantErrCount    int
	wantFieldPrefix string
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

	tests := []projectionProvideNeedsWriteCase{
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
			name:           "write CU present but no provide → no error (inverse not enforced)",
			projectionID:   "projection.order.status.v1",
			contractKind:   "projection",
			subProjection:  "order_status",
			missingProvide: true, // genuinely no read side: only the write CU exists
			wantErrCount:   0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertProjectionProvideNeedsWrite(t, tc)
		})
	}
}

func assertProjectionProvideNeedsWrite(t *testing.T, tc projectionProvideNeedsWriteCase) {
	t.Helper()

	project := buildProjectionProvideProject(
		tc.projectionID,
		tc.contractKind,
		tc.subProjection,
		tc.missingContract,
		tc.missingCell,
		tc.missingProvide,
	)

	v := NewValidator(project, "", clock.Real())
	results := v.validatePROJECTIONPROVIDENEEDSWRITECU01()

	got := projectionProvideNeedsWriteResults(results)
	if len(got) != tc.wantErrCount {
		t.Fatalf("expected %d result(s) with code %s, got %d: %v",
			tc.wantErrCount, codePROJECTIONPROVIDENEEDSWRITECU01, len(got), got)
	}
	if tc.wantErrCount == 0 {
		return
	}
	assertProjectionProvideFinding(t, got[0], tc)
}

func projectionProvideNeedsWriteResults(results []ValidationResult) []ValidationResult {
	var got []ValidationResult
	for _, r := range results {
		if r.Code == codePROJECTIONPROVIDENEEDSWRITECU01 {
			got = append(got, r)
		}
	}
	return got
}

func assertProjectionProvideFinding(t *testing.T, r ValidationResult, tc projectionProvideNeedsWriteCase) {
	t.Helper()

	if r.Severity != SeverityError {
		t.Errorf("expected SeverityError, got %s", r.Severity)
	}
	if tc.wantFieldPrefix != "" && !strings.HasPrefix(r.Field, tc.wantFieldPrefix) {
		t.Errorf("expected field with prefix %q, got %q", tc.wantFieldPrefix, r.Field)
	}
	if r.Fix == "" {
		t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
}

// TestProjectionProvideNeedsWriteCU01_SagaJournalWriteCUCounts confirms the
// provide↔subscribe write-CU pairing is SOURCE-AGNOSTIC (EPIC #1609 PR-05
// fanout audit ②): a write-side subscribe+projection CU sourced from the saga
// journal (projectionSource=saga-journal) satisfies the provide's write-side
// requirement exactly the same as an outbox-sourced one. The pairing keys on the
// projection NAME, never on the source, so this provide CU must NOT fire.
func TestProjectionProvideNeedsWriteCU01_SagaJournalWriteCUCounts(t *testing.T) {
	t.Parallel()

	cellID := metadatatest.CellIDTestCell
	const (
		projectionID = "projection.order.saga-summary.v1"
		sliceAID     = "testcell/projprovide"
		sliceBID     = "testcell/projwrite"
		sagaContract = "saga.order.v1"
	)

	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellID: {
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
		Slices: map[string]*metadata.SliceMeta{
			sliceAID: {
				ID:            sliceAID,
				BelongsToCell: metadatatest.CellIDTestCell,
				ContractUsages: []metadata.ContractUsage{
					{Contract: projectionID, Role: "provide"},
				},
				File: "cells/testcell/slices/projprovide/slice.yaml",
			},
			sliceBID: {
				ID:            sliceBID,
				BelongsToCell: metadatatest.CellIDTestCell,
				ContractUsages: []metadata.ContractUsage{
					{
						Contract:         sagaContract,
						Role:             "subscribe",
						Projection:       "order_saga_summary",
						ProjectionSource: "saga-journal",
					},
				},
				File: "cells/testcell/slices/projwrite/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			projectionID: {
				ID:               projectionID,
				Kind:             "projection",
				OwnerCell:        metadatatest.CellIDTestCell,
				ConsistencyLevel: "L3",
				Lifecycle:        "active",
				Endpoints:        metadata.EndpointsMeta{Provider: metadatatest.CellIDTestCell},
				Dir:              "contracts/projection/order/saga-summary/v1",
				File:             "contracts/projection/order/saga-summary/v1/contract.yaml",
			},
			sagaContract: {ID: sagaContract, Kind: "saga"},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, "", clock.Real())
	got := projectionProvideNeedsWriteResults(v.validatePROJECTIONPROVIDENEEDSWRITECU01())
	if len(got) != 0 {
		t.Fatalf("saga-journal-sourced subscribe+projection write CU must satisfy the "+
			"provide write-side pairing (source-agnostic); got %d finding(s): %v", len(got), got)
	}
}
