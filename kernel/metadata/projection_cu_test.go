package metadata_test

// projection_cu_test.go validates the projection:, onReset: and projectionSource:
// fields on subscribe contractUsages:
//
//   - All three fields parse correctly onto ContractUsage from YAML.
//   - validateProjectionUniqueness rejects two slices in the same cell
//     declaring the same projection id (KindConflict).
//   - onReset without projection on the same CU is rejected (KindInvalid).
//   - projection / onReset / projectionSource on a non-subscribe role is rejected
//     (KindInvalid).
//   - group on a projection subscribe CU is rejected (KindInvalid).
//   - projectionSource is required-when-projection, enum-checked, and saga-journal
//     forbids onReset (EPIC #1609 PR-05).
//   - duplicate-projection error includes the contract id in its details.
//
// Schema-level acceptance/rejection (bad projectionID pattern, non-subscribe
// role carrying projection) is covered by
// kernel/metadata/schemas/projection_schema_test.go.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// YAML unmarshal: projection + onReset + projectionSource round-trip
// ---------------------------------------------------------------------------

func TestContractUsage_ProjectionAndOnResetUnmarshal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		wantCU metadata.ContractUsage
	}{
		{
			name: "subscribe with outbox projection",
			input: `contract: event.order.placed.v1
role: subscribe
handler: HandleOrderPlaced
projection: order_read_model
projectionSource: outbox`,
			wantCU: metadata.ContractUsage{
				Contract:         "event.order.placed.v1",
				Role:             "subscribe",
				Handler:          "HandleOrderPlaced",
				Projection:       "order_read_model",
				ProjectionSource: "outbox",
			},
		},
		{
			name: "subscribe with projection, projectionSource and onReset",
			input: `contract: event.order.placed.v1
role: subscribe
handler: HandleOrderPlaced
projection: order_read_model
projectionSource: outbox
onReset: ResetOrderProjection`,
			wantCU: metadata.ContractUsage{
				Contract:         "event.order.placed.v1",
				Role:             "subscribe",
				Handler:          "HandleOrderPlaced",
				Projection:       "order_read_model",
				ProjectionSource: "outbox",
				OnReset:          "ResetOrderProjection",
			},
		},
		{
			name: "subscribe with saga-journal projection",
			input: `contract: saga.orderfulfillment.v1
role: subscribe
handler: ApplySagaTerminal
projection: order_status
projectionSource: saga-journal`,
			wantCU: metadata.ContractUsage{
				Contract:         "saga.orderfulfillment.v1",
				Role:             "subscribe",
				Handler:          "ApplySagaTerminal",
				Projection:       "order_status",
				ProjectionSource: "saga-journal",
			},
		},
		{
			name: "subscribe with no projection fields (unchanged)",
			input: `contract: event.order.placed.v1
role: subscribe
handler: HandleOrderPlaced`,
			wantCU: metadata.ContractUsage{
				Contract: "event.order.placed.v1",
				Role:     "subscribe",
				Handler:  "HandleOrderPlaced",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got metadata.ContractUsage
			require.NoError(t, yaml.Unmarshal([]byte(tt.input), &got))
			assert.Equal(t, tt.wantCU, got)
		})
	}
}

// ---------------------------------------------------------------------------
// validateProjectionUniqueness: duplicate projection within a cell
// ---------------------------------------------------------------------------

// buildProjectionProject creates a minimal ProjectMeta for projection uniqueness tests.
func buildProjectionProject(slices map[string]*metadata.SliceMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     slices,
		Contracts:  make(map[string]*metadata.ContractMeta),
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
}

// outboxProjectionCU is a valid outbox-sourced projection subscribe CU helper.
func outboxProjectionCU(contract, projection string) metadata.ContractUsage {
	return metadata.ContractUsage{
		Contract:         contract,
		Role:             "subscribe",
		Handler:          "Handle",
		Projection:       projection,
		ProjectionSource: "outbox",
	}
}

// TestValidateProjectionUniqueness_DuplicateWithinCell verifies that two slices
// in the same cell using the same projection id cause Parse to return
// KindConflict.
func TestValidateProjectionUniqueness_DuplicateWithinCell(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:             "orderquery",
			BelongsToCell:  metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.placed.v1", "order_read_model")},
		},
		"ordercell/orderstatus": {
			ID:             "orderstatus",
			BelongsToCell:  metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.updated.v1", "order_read_model")}, // same id, same cell — conflict
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "duplicate projection id in same cell must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindConflict, ecErr.Kind,
		"duplicate projection id must produce KindConflict")
}

// TestValidateProjectionUniqueness_SameIDDifferentCells verifies that the same
// projection id used in two DIFFERENT cells is allowed.
func TestValidateProjectionUniqueness_SameIDDifferentCells(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:             "orderquery",
			BelongsToCell:  metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.placed.v1", "order_read_model")},
		},
		"inventorycell/invquery": {
			ID:             "invquery",
			BelongsToCell:  metadatatest.NewCellID("inventorycell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.placed.v1", "order_read_model")}, // same id, different cell — OK
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	assert.NoError(t, err, "same projection id in different cells must be allowed")
}

// TestValidateProjectionUniqueness_SameSliceDuplicateProjection verifies that
// a single slice declaring the same projection id in two CUs is also a conflict
// (a slice's own projections must be distinct).
func TestValidateProjectionUniqueness_SameSliceDuplicateProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				outboxProjectionCU("event.order.placed.v1", "order_read_model"),
				outboxProjectionCU("event.order.cancelled.v1", "order_read_model"), // same id in same slice — conflict
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "same projection id in two CUs of the same slice must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindConflict, ecErr.Kind)
}

// TestValidateProjectionUniqueness_NoProjection verifies that slices without
// projection fields pass validation without error.
func TestValidateProjectionUniqueness_NoProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract: "event.order.placed.v1",
					Role:     "subscribe",
					Handler:  "Handle",
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	assert.NoError(t, err, "CUs without projection must not cause an error")
}

// ---------------------------------------------------------------------------
// onReset without projection: KindInvalid
// ---------------------------------------------------------------------------

// TestValidateProjectionUniqueness_OnResetWithoutProjection verifies that a
// CU carrying onReset but no projection on the same CU is rejected with
// KindInvalid.
func TestValidateProjectionUniqueness_OnResetWithoutProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract: "event.order.placed.v1",
					Role:     "subscribe",
					Handler:  "Handle",
					OnReset:  "ResetProjection", // no Projection — invalid
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "onReset without projection on the same CU must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind,
		"onReset without projection must produce KindInvalid")
}

// TestValidateProjectionUniqueness_OnResetWithProjection verifies that a CU
// with projection, projectionSource=outbox and onReset set passes validation.
func TestValidateProjectionUniqueness_OnResetWithProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "event.order.placed.v1",
					Role:             "subscribe",
					Handler:          "Handle",
					Projection:       "order_read_model",
					ProjectionSource: "outbox",
					OnReset:          "ResetProjection",
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	assert.NoError(t, err, "CU with projection, outbox source and onReset must pass validation")
}

// ---------------------------------------------------------------------------
// F1: projection/onReset/projectionSource on non-subscribe CU must be rejected
// ---------------------------------------------------------------------------

// TestValidateProjectionUniqueness_NonSubscribeWithProjection verifies that a
// non-subscribe CU (e.g. role=provide) carrying a projection field is rejected
// with KindInvalid, even though the schema may not run in the validate path.
func TestValidateProjectionUniqueness_NonSubscribeWithProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderprovide": {
			ID:            "orderprovide",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "data.order.read.v1",
					Role:       "provide",
					Projection: "order_read_model", // invalid: non-subscribe role
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "projection on non-subscribe role must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind,
		"projection on non-subscribe role must produce KindInvalid")
}

// TestValidateProjectionUniqueness_NonSubscribeWithOnReset verifies that a
// non-subscribe CU carrying onReset is rejected with KindInvalid.
func TestValidateProjectionUniqueness_NonSubscribeWithOnReset(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderprovide": {
			ID:            "orderprovide",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract: "data.order.read.v1",
					Role:     "serve",
					OnReset:  "ResetModel", // invalid: non-subscribe role
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "onReset on non-subscribe role must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind,
		"onReset on non-subscribe role must produce KindInvalid")
}

// TestValidateProjectionUniqueness_NonSubscribeWithProjectionSource verifies that
// a non-subscribe CU carrying projectionSource is rejected with KindInvalid (F1).
func TestValidateProjectionUniqueness_NonSubscribeWithProjectionSource(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderprovide": {
			ID:            "orderprovide",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "data.order.read.v1",
					Role:             "provide",
					ProjectionSource: "saga-journal", // invalid: non-subscribe role
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "projectionSource on non-subscribe role must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
}

// ---------------------------------------------------------------------------
// F-source: projectionSource required-when-projection, enum, saga-journal rules
// ---------------------------------------------------------------------------

// TestProjectionSource_RequiredWhenProjection verifies that a subscribe CU with
// projection but no projectionSource is rejected (no implicit default).
func TestProjectionSource_RequiredWhenProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "event.order.placed.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model", // no projectionSource — invalid
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "projection without projectionSource must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
	assert.Contains(t, ecErr.Message, "projectionSource is required")
}

// TestProjectionSource_WithoutProjection verifies that a subscribe CU with
// projectionSource but no projection is rejected.
func TestProjectionSource_WithoutProjection(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "event.order.placed.v1",
					Role:             "subscribe",
					Handler:          "Handle",
					ProjectionSource: "outbox", // no projection — invalid
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "projectionSource without projection must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
}

// TestProjectionSource_InvalidEnum verifies an unknown projectionSource value is rejected.
func TestProjectionSource_InvalidEnum(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "event.order.placed.v1",
					Role:             "subscribe",
					Handler:          "Handle",
					Projection:       "order_read_model",
					ProjectionSource: "kafka", // not in {outbox, saga-journal}
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "invalid projectionSource enum value must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
	assert.Contains(t, ecErr.Message, "projectionSource must be one of")
}

// TestProjectionSource_SagaJournalForbidsOnReset verifies that onReset is rejected
// on a saga-journal projection (no rebuild on that path).
func TestProjectionSource_SagaJournalForbidsOnReset(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"orderfulfillmentcell/sagastatus": {
			ID:            "sagastatus",
			BelongsToCell: metadatatest.NewCellID("orderfulfillmentcell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "saga.orderfulfillment.v1",
					Role:             "subscribe",
					Handler:          "ApplySagaTerminal",
					Projection:       "order_status",
					ProjectionSource: "saga-journal",
					OnReset:          "ResetOrderStatus", // forbidden on saga-journal
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "onReset on saga-journal projection must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
	assert.Contains(t, ecErr.Message, "onReset is not allowed")
}

// TestProjectionSource_SagaJournalHappy verifies a saga-journal projection (no
// onReset) passes validation.
func TestProjectionSource_SagaJournalHappy(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"orderfulfillmentcell/sagastatus": {
			ID:            "sagastatus",
			BelongsToCell: metadatatest.NewCellID("orderfulfillmentcell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "saga.orderfulfillment.v1",
					Role:             "subscribe",
					Handler:          "ApplySagaTerminal",
					Projection:       "order_status",
					ProjectionSource: "saga-journal",
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	assert.NoError(t, err, "saga-journal projection without onReset must pass validation")
}

// ---------------------------------------------------------------------------
// F2: group on a projection subscribe CU must be rejected (fail-closed)
// ---------------------------------------------------------------------------

// TestValidateProjectionUniqueness_ProjectionWithGroup verifies that a
// subscribe CU with both projection and group set is rejected with KindInvalid.
// The consumer group for a projection CU is derived from cellID + projectionID
// by cellgen; a hand-written group: is dead config and must be rejected.
func TestValidateProjectionUniqueness_ProjectionWithGroup(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:         "event.order.placed.v1",
					Role:             "subscribe",
					Handler:          "Handle",
					Projection:       "order_read_model",
					ProjectionSource: "outbox",
					Group:            "my-custom-group", // invalid: group forbidden on projection CU
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err, "group on projection subscribe CU must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind,
		"group on projection CU must produce KindInvalid")
}

// ---------------------------------------------------------------------------
// F9: duplicate-projection error must include the contract id in details
// ---------------------------------------------------------------------------

// TestValidateProjectionUniqueness_DuplicateErrorIncludesContract verifies
// that the KindConflict error for a duplicate projection id includes the
// contract id in its details for better localization.
func TestValidateProjectionUniqueness_DuplicateErrorIncludesContract(t *testing.T) {
	t.Parallel()
	pm := buildProjectionProject(map[string]*metadata.SliceMeta{
		"ordercell/orderquery": {
			ID:             "orderquery",
			BelongsToCell:  metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.placed.v1", "order_read_model")},
		},
		"ordercell/orderstatus": {
			ID:             "orderstatus",
			BelongsToCell:  metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{outboxProjectionCU("event.order.updated.v1", "order_read_model")}, // duplicate — triggers conflict
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindConflict, ecErr.Kind)

	// F9: the duplicate-projection error must include the contract id in its
	// public details so operators can locate the conflicting declaration.
	_, found := ecErr.FindAttr("contract")
	assert.True(t, found, "duplicate-projection error must include 'contract' in its public details")
}
