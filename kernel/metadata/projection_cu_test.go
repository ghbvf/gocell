package metadata_test

// projection_cu_test.go validates the projection: and onReset: fields on
// subscribe contractUsages:
//
//   - Both fields parse correctly onto ContractUsage from YAML.
//   - validateProjectionUniqueness rejects two slices in the same cell
//     declaring the same projection id (KindConflict).
//   - onReset without projection on the same CU is rejected (KindInvalid).
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
// YAML unmarshal: projection + onReset round-trip
// ---------------------------------------------------------------------------

func TestContractUsage_ProjectionAndOnResetUnmarshal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		wantCU metadata.ContractUsage
	}{
		{
			name: "subscribe with projection only",
			input: `contract: event.order.placed.v1
role: subscribe
handler: HandleOrderPlaced
projection: order_read_model`,
			wantCU: metadata.ContractUsage{
				Contract:   "event.order.placed.v1",
				Role:       "subscribe",
				Handler:    "HandleOrderPlaced",
				Projection: "order_read_model",
			},
		},
		{
			name: "subscribe with projection and onReset",
			input: `contract: event.order.placed.v1
role: subscribe
handler: HandleOrderPlaced
projection: order_read_model
onReset: ResetOrderProjection`,
			wantCU: metadata.ContractUsage{
				Contract:   "event.order.placed.v1",
				Role:       "subscribe",
				Handler:    "HandleOrderPlaced",
				Projection: "order_read_model",
				OnReset:    "ResetOrderProjection",
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

// TestValidateProjectionUniqueness_DuplicateWithinCell verifies that two slices
// in the same cell using the same projection id cause Parse to return
// KindConflict.
func TestValidateProjectionUniqueness_DuplicateWithinCell(t *testing.T) {
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
					Projection: "order_read_model",
				},
			},
		},
		"ordercell/orderstatus": {
			ID:            "orderstatus",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "event.order.updated.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model", // same id, same cell — conflict
				},
			},
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
			ID:            "orderquery",
			BelongsToCell: metadatatest.NewCellID("ordercell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "event.order.placed.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model",
				},
			},
		},
		"inventorycell/invquery": {
			ID:            "invquery",
			BelongsToCell: metadatatest.NewCellID("inventorycell"),
			ContractUsages: []metadata.ContractUsage{
				{
					Contract:   "event.order.placed.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model", // same id, different cell — OK
				},
			},
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
				{
					Contract:   "event.order.placed.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model",
				},
				{
					Contract:   "event.order.cancelled.v1",
					Role:       "subscribe",
					Handler:    "Handle",
					Projection: "order_read_model", // same id in same slice — conflict
				},
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
// with both projection and onReset set passes uniqueness validation.
func TestValidateProjectionUniqueness_OnResetWithProjection(t *testing.T) {
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
					Projection: "order_read_model",
					OnReset:    "ResetProjection",
				},
			},
		},
	})

	err := metadata.ExportedValidateProjectionUniqueness(pm)
	assert.NoError(t, err, "CU with both projection and onReset must pass validation")
}
