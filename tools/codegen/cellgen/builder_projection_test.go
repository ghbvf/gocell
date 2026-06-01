package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// eventOrderCreatedContract returns a minimal event contract for order-created,
// usable as a fixture in projection happy-path tests.
func eventOrderCreatedContract() *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:   "event.order-created.v1",
		Kind: "event",
	}
}

// TestBuildProjections_HappyPath verifies that a subscribe CU with a
// non-empty Projection field produces a ProjectionGenSpec with the correct
// ContractID, SliceID, ProjectionID, ApplyExpr, and OnResetExpr.
func TestBuildProjections_HappyPath(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "ordercell",
		Dir:          "ordercell",
		File:         "cells/ordercell/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("OrderCell"),
	}
	slc := &metadata.SliceMeta{
		ID:            "orderprojection",
		BelongsToCell: "ordercell",
		Dir:           "orderprojection",
		File:          "cells/ordercell/slices/orderprojection/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrderCreated",
				Projection: "order_status",
				OnReset:    "ResetOrderStatus",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"orderprojection": "projSvc"})

	spec, err := BuildCellSpec(p, "ordercell", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Projections) != 1 {
		t.Fatalf("Projections len = %d, want 1", len(spec.Projections))
	}
	pr := spec.Projections[0]
	if pr.ContractID != "event.order-created.v1" {
		t.Errorf("ContractID = %q, want event.order-created.v1", pr.ContractID)
	}
	if pr.SliceID != "orderprojection" {
		t.Errorf("SliceID = %q, want orderprojection", pr.SliceID)
	}
	if pr.ProjectionID != "order_status" {
		t.Errorf("ProjectionID = %q, want order_status", pr.ProjectionID)
	}
	if pr.ApplyExpr != "c.projSvc.HandleOrderCreated" {
		t.Errorf("ApplyExpr = %q, want c.projSvc.HandleOrderCreated", pr.ApplyExpr)
	}
	if pr.OnResetExpr != "c.projSvc.ResetOrderStatus" {
		t.Errorf("OnResetExpr = %q, want c.projSvc.ResetOrderStatus", pr.OnResetExpr)
	}
}

// TestBuildProjections_OnResetEmpty verifies that omitting OnReset produces
// an empty OnResetExpr (renders nil in the template).
func TestBuildProjections_OnResetEmpty(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "ordercell",
		Dir:          "ordercell",
		File:         "cells/ordercell/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("OrderCell"),
	}
	slc := &metadata.SliceMeta{
		ID:            "orderprojection",
		BelongsToCell: "ordercell",
		Dir:           "orderprojection",
		File:          "cells/ordercell/slices/orderprojection/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrderCreated",
				Projection: "order_status",
				// OnReset intentionally omitted
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"orderprojection": "projSvc"})

	spec, err := BuildCellSpec(p, "ordercell", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Projections) != 1 {
		t.Fatalf("Projections len = %d, want 1", len(spec.Projections))
	}
	if got := spec.Projections[0].OnResetExpr; got != "" {
		t.Errorf("OnResetExpr = %q, want empty string when OnReset is omitted", got)
	}
}

// TestBuildProjections_PartitionDisjointness is the key regression test: a
// slice with two subscribe CUs — one with Projection set and one without —
// must produce exactly one entry in spec.Projections (the projection CU) and
// exactly one entry in spec.Subscriptions (the plain subscribe CU).
func TestBuildProjections_PartitionDisjointness(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "ordercell",
		Dir:          "ordercell",
		File:         "cells/ordercell/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("OrderCell"),
	}
	slc := &metadata.SliceMeta{
		ID:            "mixedslice",
		BelongsToCell: "ordercell",
		Dir:           "mixedslice",
		File:          "cells/ordercell/slices/mixedslice/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrderCreated",
				Projection: "order_status", // projection CU
			},
			{
				Contract: "event.order-created.v1",
				Role:     "subscribe",
				Handler:  "HandleOrderCreatedAudit",
				// No Projection — plain subscribe CU
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"mixedslice": "mixedSvc"})

	spec, err := BuildCellSpec(p, "ordercell", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	// Exactly one projection.
	if len(spec.Projections) != 1 {
		t.Fatalf("Projections len = %d, want 1; disjointness broken", len(spec.Projections))
	}
	if spec.Projections[0].ProjectionID != "order_status" {
		t.Errorf("Projections[0].ProjectionID = %q, want order_status", spec.Projections[0].ProjectionID)
	}
	// Exactly one plain subscription.
	if len(spec.Subscriptions) != 1 {
		t.Fatalf("Subscriptions len = %d, want 1; disjointness broken", len(spec.Subscriptions))
	}
	if spec.Subscriptions[0].HandlerExpr != "c.mixedSvc.HandleOrderCreatedAudit" {
		t.Errorf("Subscriptions[0].HandlerExpr = %q, want c.mixedSvc.HandleOrderCreatedAudit", spec.Subscriptions[0].HandlerExpr)
	}
}

// TestBuildProjections_SortBySliceIDThenProjectionID verifies that projections
// are sorted by (SliceID, ProjectionID) across two slices, mirroring the
// subscription sort order.
func TestBuildProjections_SortBySliceIDThenProjectionID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           "ordercell",
		Dir:          "ordercell",
		File:         "cells/ordercell/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("OrderCell"),
	}
	// alphaprojslice (low SliceID) has projection "zzz_status";
	// zebraprojslice (high SliceID) has projection "aaa_status".
	// Primary sort is SliceID so alpha comes first regardless of projection id.
	alphaSlc := &metadata.SliceMeta{
		ID:            "alphaprojslice",
		BelongsToCell: "ordercell",
		Dir:           "alphaprojslice",
		File:          "cells/ordercell/slices/alphaprojslice/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrder",
				Projection: "zzz_status",
			},
		},
	}
	zebraSlc := &metadata.SliceMeta{
		ID:            "zebraprojslice",
		BelongsToCell: "ordercell",
		Dir:           "zebraprojslice",
		File:          "cells/ordercell/slices/zebraprojslice/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrder",
				Projection: "aaa_status",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{alphaSlc, zebraSlc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{
		"alphaprojslice": "alphaSvc",
		"zebraprojslice": "zebraSvc",
	})

	spec, err := BuildCellSpec(p, "ordercell", markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Projections) != 2 {
		t.Fatalf("Projections len = %d, want 2", len(spec.Projections))
	}
	// Primary sort key is SliceID.
	if got := spec.Projections[0].SliceID; got != "alphaprojslice" {
		t.Errorf("Projections[0].SliceID = %q, want alphaprojslice (sorted by SliceID)", got)
	}
	if got := spec.Projections[1].SliceID; got != "zebraprojslice" {
		t.Errorf("Projections[1].SliceID = %q, want zebraprojslice (sorted by SliceID)", got)
	}
}

// TestBuildProjections_InvalidProjectionID verifies that a projection id that
// does not match snake_case pattern is rejected.
func TestBuildProjections_InvalidProjectionID(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "projsvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "projsvc",
		File:          "cells/demo/slices/projsvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleOrder",
				Projection: "Order-Status", // invalid: uppercase + hyphen
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"projsvc": "projSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for invalid projection id, got nil")
	}
	if !strings.Contains(err.Error(), "projection") {
		t.Errorf("error should mention projection, got: %v", err)
	}
}

// TestBuildProjections_InvalidHandler verifies that a projection CU with a
// non-exported handler identifier is rejected.
func TestBuildProjections_InvalidHandler(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "projsvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "projsvc",
		File:          "cells/demo/slices/projsvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "handleOrder", // lowercase — invalid exported ident
				Projection: "order_status",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"projsvc": "projSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for non-exported handler, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "handler") {
		t.Errorf("error should mention handler, got: %v", err)
	}
}

// TestBuildProjections_MissingContract verifies that a projection CU
// referencing an unknown contract is rejected.
func TestBuildProjections_MissingContract(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "projsvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "projsvc",
		File:          "cells/demo/slices/projsvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "event.ghost.v1", // does not exist
				Role:       "subscribe",
				Handler:    "HandleGhost",
				Projection: "ghost_status",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil) // no contracts
	fieldIndex := idxOf(map[string]string{"projsvc": "projSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for missing contract, got nil")
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("error should mention contract, got: %v", err)
	}
}

// TestBuildProjections_NonEventContract verifies that a projection CU targeting
// a non-event contract kind is rejected.
func TestBuildProjections_NonEventContract(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "projsvc",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "projsvc",
		File:          "cells/demo/slices/projsvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			{
				Contract:   "http.orders.v1", // wrong kind
				Role:       "subscribe",
				Handler:    "HandleOrder",
				Projection: "order_status",
			},
		},
	}
	httpContract := &metadata.ContractMeta{
		ID:   "http.orders.v1",
		Kind: "http",
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{httpContract})
	fieldIndex := idxOf(map[string]string{"projsvc": "projSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for non-event contract, got nil")
	}
	if !strings.Contains(err.Error(), "non-event") {
		t.Errorf("error should mention non-event, got: %v", err)
	}
}

// TestEnrichProjectionsWithModulePath verifies that EnrichProjectionsWithModulePath
// populates SpecPackage and SpecAlias on each ProjectionGenSpec.
func TestEnrichProjectionsWithModulePath(t *testing.T) {
	t.Parallel()
	spec := &CellGenSpec{
		Projections: []ProjectionGenSpec{
			{ContractID: "event.order-created.v1"},
		},
	}
	EnrichProjectionsWithModulePath(spec, "github.com/ghbvf/gocell")
	if len(spec.Projections) != 1 {
		t.Fatalf("Projections len = %d, want 1", len(spec.Projections))
	}
	pr := spec.Projections[0]
	if pr.SpecAlias != "proj0" {
		t.Errorf("SpecAlias = %q, want proj0", pr.SpecAlias)
	}
	if pr.SpecPackage == "" {
		t.Errorf("SpecPackage is empty; EnrichProjectionsWithModulePath must populate it")
	}
	if !strings.Contains(pr.SpecPackage, "event") {
		t.Errorf("SpecPackage = %q; expected it to contain 'event'", pr.SpecPackage)
	}
}
