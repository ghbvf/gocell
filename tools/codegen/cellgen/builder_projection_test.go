package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
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

// TestBuildProjections_InvalidOnReset verifies that a projection CU with a
// non-exported onReset identifier (lowercase first letter) is rejected.
func TestBuildProjections_InvalidOnReset(t *testing.T) {
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
				Handler:    "HandleX",
				Projection: "order_status",
				OnReset:    "resetOrder", // lowercase first letter — invalid exported ident
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	fieldIndex := idxOf(map[string]string{"projsvc": "projSvc"})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for invalid onReset identifier, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "onreset") {
		t.Errorf("error should mention onReset, got: %v", err)
	}
}

// TestBuildProjections_FieldAmbiguity verifies that a projection CU whose
// slice maps to an ambiguous field in the CellFieldIndex (multiple fields
// whose pointer-type package name matches the sliceID) is rejected.
func TestBuildProjections_FieldAmbiguity(t *testing.T) {
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
				Handler:    "HandleOrderCreated",
				Projection: "order_status",
				// No Field: set — ambiguity should fail
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})
	// Ambiguous: two *projsvc.T fields in the cell struct.
	fieldIndex := &CellFieldIndex{
		byPkg:   map[string]string{"projsvc": ambiguousField},
		byField: map[string]string{"projSvcA": "projsvc", "projSvcB": "projsvc"},
	}

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "disambiguate") && !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity/disambiguation, got: %v", err)
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

// TestBuildSliceSpec_ExcludesProjectionHandlers is the key F8 regression test:
// a slice with two subscribe CUs — one plain (no Projection) and one projection
// CU (Projection != "") — must produce a SliceGenSpec whose Handlers list
// contains ONLY the plain handler. The projection handler must NOT appear in
// the eventHandlerService interface (its signature is enforced structurally via
// cell.ProjectionApply at the reg.RegisterProjection callsite, not via the
// HandleResult interface).
//
// Before the fix BuildSliceSpec collects ALL subscribe CUs into Handlers,
// so the projection handler would be rendered into eventHandlerService with
// "...HandleResult" signature — making the generated slice unable to compile
// when the service also satisfies "...error" for the same method name.
func TestBuildSliceSpec_ExcludesProjectionHandlers(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "mixedslice",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "mixedslice",
		File:          "cells/demo/slices/mixedslice/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			// Plain subscribe CU — must appear in eventHandlerService.
			{
				Contract: "event.order-created.v1",
				Role:     "subscribe",
				Handler:  "HandleX",
			},
			// Projection CU — must NOT appear in eventHandlerService.
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleY",
				Projection: "py",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildSliceSpec(p, metadatatest.CellIDDemo, "mixedslice")
	if err != nil {
		t.Fatalf("BuildSliceSpec: %v", err)
	}
	if len(spec.Handlers) != 1 {
		t.Fatalf("Handlers len = %d, want 1 (projection handler HandleY must be excluded); got %v",
			len(spec.Handlers), spec.Handlers)
	}
	if spec.Handlers[0].MethodName != "HandleX" {
		t.Errorf("Handlers[0].MethodName = %q, want HandleX", spec.Handlers[0].MethodName)
	}
	// Assert projection handler is NOT in the list at all.
	for _, h := range spec.Handlers {
		if h.MethodName == "HandleY" {
			t.Errorf("projection handler HandleY must not appear in eventHandlerService Handlers; got %v", spec.Handlers)
		}
	}
}

// TestBuildSliceSpec_OnlyProjectionCUProducesZeroHandlers verifies that a slice
// with ONLY a projection CU (no plain subscribe) produces zero Handlers, so
// slice.tmpl omits the eventHandlerService block entirely.
func TestBuildSliceSpec_OnlyProjectionCUProducesZeroHandlers(t *testing.T) {
	t.Parallel()
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:            "projonly",
		BelongsToCell: metadatatest.CellIDDemo,
		Dir:           "projonly",
		File:          "cells/demo/slices/projonly/slice.yaml",
		ContractUsages: []metadata.ContractUsage{
			// Only a projection CU — no plain subscribe.
			{
				Contract:   "event.order-created.v1",
				Role:       "subscribe",
				Handler:    "HandleZ",
				Projection: "pz",
			},
		},
	}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, nil)

	spec, err := BuildSliceSpec(p, metadatatest.CellIDDemo, "projonly")
	if err != nil {
		t.Fatalf("BuildSliceSpec: %v", err)
	}
	if len(spec.Handlers) != 0 {
		t.Fatalf("Handlers len = %d, want 0 for projection-only slice; got %v",
			len(spec.Handlers), spec.Handlers)
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

// projectionFixtureCellSlice returns a demo cell + a single-CU slice carrying the
// given projection contractUsage, plus the field index mapping the slice package
// to a cell struct field. Shared by the saga/outbox source tests.
func projectionFixtureCellSlice(cu metadata.ContractUsage) (*metadata.CellMeta, *metadata.SliceMeta, *CellFieldIndex) {
	cell := &metadata.CellMeta{
		ID:           metadatatest.CellIDDemo,
		Dir:          "demo",
		File:         "cells/demo/cell.yaml",
		GoStructName: metadata.MustNewGoIdentifier("Demo"),
	}
	slc := &metadata.SliceMeta{
		ID:             "projsvc",
		BelongsToCell:  metadatatest.CellIDDemo,
		Dir:            "projsvc",
		File:           "cells/demo/slices/projsvc/slice.yaml",
		ContractUsages: []metadata.ContractUsage{cu},
	}
	return cell, slc, idxOf(map[string]string{"projsvc": "projSvc"})
}

// TestBuildProjections_SagaJournalSource verifies that a subscribe CU with
// projectionSource=saga-journal consuming a kind=saga contract produces a
// ProjectionGenSpec with Source=="saga-journal", ContractID set, and (after
// enrichment) no SpecPackage — the saga path references no per-contract import.
func TestBuildProjections_SagaJournalSource(t *testing.T) {
	t.Parallel()
	cell, slc, fieldIndex := projectionFixtureCellSlice(metadata.ContractUsage{
		Contract:         "saga.orderfulfillment.v1",
		Role:             "subscribe",
		Handler:          "ApplySagaTerminal",
		Projection:       "order_status",
		ProjectionSource: "saga-journal",
	})
	sagaContract := &metadata.ContractMeta{ID: "saga.orderfulfillment.v1", Kind: "saga"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{sagaContract})

	spec, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err != nil {
		t.Fatalf("BuildCellSpec: %v", err)
	}
	if len(spec.Projections) != 1 {
		t.Fatalf("Projections len = %d, want 1", len(spec.Projections))
	}
	pr := spec.Projections[0]
	if pr.Source != "saga-journal" {
		t.Errorf("Source = %q, want saga-journal", pr.Source)
	}
	if pr.ContractID != "saga.orderfulfillment.v1" {
		t.Errorf("ContractID = %q, want saga.orderfulfillment.v1", pr.ContractID)
	}
	if pr.ApplyExpr != "c.projSvc.ApplySagaTerminal" {
		t.Errorf("ApplyExpr = %q, want c.projSvc.ApplySagaTerminal", pr.ApplyExpr)
	}

	// Enrichment must skip saga-journal specs — they reference no generated
	// event-contract package, so SpecPackage/SpecAlias stay empty.
	EnrichProjectionsWithModulePath(spec, "github.com/ghbvf/gocell")
	if spec.Projections[0].SpecPackage != "" {
		t.Errorf("SpecPackage = %q, want empty for saga-journal source", spec.Projections[0].SpecPackage)
	}
	if spec.Projections[0].SpecAlias != "" {
		t.Errorf("SpecAlias = %q, want empty for saga-journal source", spec.Projections[0].SpecAlias)
	}
}

// TestBuildProjections_SagaJournalRejectsEventContract verifies that
// projectionSource=saga-journal pointing at a kind=event contract is rejected:
// a saga-journal projection must consume a saga contract.
func TestBuildProjections_SagaJournalRejectsEventContract(t *testing.T) {
	t.Parallel()
	cell, slc, fieldIndex := projectionFixtureCellSlice(metadata.ContractUsage{
		Contract:         "event.order-created.v1",
		Role:             "subscribe",
		Handler:          "ApplySagaTerminal",
		Projection:       "order_status",
		ProjectionSource: "saga-journal",
	})
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for saga-journal source on event contract, got nil")
	}
	if !strings.Contains(err.Error(), "saga-journal projection must consume a saga contract") {
		t.Errorf("error should mention saga-journal/saga contract, got: %v", err)
	}
}

// TestBuildProjections_OutboxRejectsSagaContract verifies that the default/outbox
// projection source pointing at a kind=saga contract is rejected: the outbox path
// must consume an event contract.
func TestBuildProjections_OutboxRejectsSagaContract(t *testing.T) {
	t.Parallel()
	cell, slc, fieldIndex := projectionFixtureCellSlice(metadata.ContractUsage{
		Contract:         "saga.orderfulfillment.v1",
		Role:             "subscribe",
		Handler:          "HandleOrder",
		Projection:       "order_status",
		ProjectionSource: "outbox",
	})
	sagaContract := &metadata.ContractMeta{ID: "saga.orderfulfillment.v1", Kind: "saga"}
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{sagaContract})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for outbox source on saga contract, got nil")
	}
	if !strings.Contains(err.Error(), "non-event") {
		t.Errorf("error should mention non-event, got: %v", err)
	}
}

// TestBuildProjections_UnknownSourceFailsClosed is the synthetic red case for the
// builder's fail-closed default: an unrecognized projectionSource must NOT silently
// fall through to the outbox path. The parser/schema reject unknown sources
// upstream, so this exercises the builder gate directly (the last funnel line) by
// feeding metadata the parser would never produce.
func TestBuildProjections_UnknownSourceFailsClosed(t *testing.T) {
	t.Parallel()
	cell, slc, fieldIndex := projectionFixtureCellSlice(metadata.ContractUsage{
		Contract:         "event.order-created.v1",
		Role:             "subscribe",
		Handler:          "HandleOrder",
		Projection:       "order_status",
		ProjectionSource: "bogus-source",
	})
	p := fixtureProject(cell, []*metadata.SliceMeta{slc}, []*metadata.ContractMeta{eventOrderCreatedContract()})

	_, err := BuildCellSpec(p, metadatatest.CellIDDemo, markergen.WireBundle{}, fieldIndex)
	if err == nil {
		t.Fatal("expected error for unknown projectionSource, got nil (must not fall through to outbox)")
	}
	if !strings.Contains(err.Error(), "unknown projectionSource") {
		t.Errorf("error should mention unknown projectionSource, got: %v", err)
	}
}
