package governance

// INVARIANT: PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// buildSagaSourceProject constructs a minimal *metadata.ProjectMeta with one
// cell, one slice and a single contractUsage, plus a contract of the requested
// kind, for PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01 tests.
func buildSagaSourceProject(contractKind string, cu metadata.ContractUsage) *metadata.ProjectMeta {
	const contractID = "test.contract.v1"
	cu.Contract = contractID
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:               metadatatest.CellIDTestCell,
				ConsistencyLevel: "L3",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Dir:              "cells/testcell",
				File:             "cells/testcell/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"testcell/testslice": {
				ID:             "testslice",
				BelongsToCell:  metadatatest.CellIDTestCell,
				ContractUsages: []metadata.ContractUsage{cu},
				File:           "cells/testcell/slices/testslice/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			contractID: {
				ID:   contractID,
				Kind: contractKind,
				File: "contracts/" + contractKind + "/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestProjectionSagaSourceNeedsProjection is the anti-vacuity table for
// PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01: two firing directions and two
// non-firing happy paths.
func TestProjectionSagaSourceNeedsProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		contractKind string
		cu           metadata.ContractUsage
		wantErrCount int
		wantField    string
	}{
		{
			// Direction (a) fires: subscribe on a saga contract with no projection.
			name:         "subscribe + saga, no projection → fires",
			contractKind: "saga",
			cu:           metadata.ContractUsage{Role: "subscribe", Handler: "OnSaga"},
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			// Direction (b) fires: saga-journal source on a non-saga (event) contract.
			name:         "projectionSource=saga-journal on non-saga contract → fires",
			contractKind: "event",
			cu: metadata.ContractUsage{
				Role: "subscribe", Handler: "OnEvent",
				Projection: "order_status", ProjectionSource: "saga-journal",
			},
			wantErrCount: 1,
			wantField:    "contractUsages[0].projectionSource",
		},
		{
			// Happy saga-journal path: subscribe + saga + projection + saga-journal.
			name:         "subscribe + saga + projection + saga-journal → no error",
			contractKind: "saga",
			cu: metadata.ContractUsage{
				Role: "subscribe", Handler: "OnSaga",
				Projection: "order_saga_summary", ProjectionSource: "saga-journal",
			},
			wantErrCount: 0,
		},
		{
			// Normal outbox projection: subscribe + event + projection + outbox.
			name:         "subscribe + event + projection + outbox → no error",
			contractKind: "event",
			cu: metadata.ContractUsage{
				Role: "subscribe", Handler: "OnEvent",
				Projection: "order_status", ProjectionSource: "outbox",
			},
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaSourceProject(tc.contractKind, tc.cu)
			v := NewValidator(project, "", clock.Real())
			got := findByCode(
				v.validatePROJECTIONSAGASOURCENEEDSPROJECTION01(),
				codePROJECTIONSAGASOURCENEEDSPROJECTION01,
			)
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d finding(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount == 0 {
				return
			}
			if got[0].Field != tc.wantField {
				t.Errorf("expected field %q, got %q", tc.wantField, got[0].Field)
			}
			if got[0].Severity != SeverityError {
				t.Errorf("expected SeverityError, got %v", got[0].Severity)
			}
			if got[0].Fix == "" {
				t.Error("finding must carry non-empty Fix guidance (typed-Fix contract)")
			}
		})
	}
}

// TestProjectionSagaSourceNeedsProjection_MissingContractSkips confirms the rule
// skips a CU whose referenced contract is absent (REF owns the missing-contract
// finding) — the same ownership boundary as TOPO-01 / SAGA-CELL-LEVEL-L3-DECLARE-01.
func TestProjectionSagaSourceNeedsProjection_MissingContractSkips(t *testing.T) {
	t.Parallel()

	project := buildSagaSourceProject("saga", metadata.ContractUsage{Role: "subscribe", Handler: "OnSaga"})
	// Drop the contract so the reference dangles.
	project.Contracts = map[string]*metadata.ContractMeta{}

	v := NewValidator(project, "", clock.Real())
	got := findByCode(
		v.validatePROJECTIONSAGASOURCENEEDSPROJECTION01(),
		codePROJECTIONSAGASOURCENEEDSPROJECTION01,
	)
	if len(got) != 0 {
		t.Fatalf("missing contract must be skipped (REF owns), got %d finding(s): %v", len(got), got)
	}
}
