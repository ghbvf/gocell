package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// TestCellSpec_PhaseRoundTrip verifies CellMeta.Lifecycle / SliceMeta.Lifecycle
// surface on the catalog wire as CellSpec.Phase / SliceSpec.Phase — the
// real consumer of the lifecycle metadata.
func TestCellSpec_PhaseRoundTrip(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"configcore": {ID: "configcore", Type: "core", ConsistencyLevel: "L2", Lifecycle: "asset"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"configcore/flagwrite": {
				ID: "flagwrite", BelongsToCell: "configcore",
				ConsistencyLevel: "L1", Lifecycle: "candidate",
			},
		},
	}
	doc, err := catalog.BuildDocument(pm, catalog.ExportOptions{
		Clock: clockmock.New(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)

	var gotCell, gotSlice bool
	for _, e := range doc.Entities {
		switch e.Kind {
		case "Cell":
			spec, ok := e.Spec.(catalog.CellSpec)
			require.True(t, ok)
			assert.Equal(t, "asset", spec.Phase)
			gotCell = true
		case "Slice":
			spec, ok := e.Spec.(catalog.SliceSpec)
			require.True(t, ok)
			assert.Equal(t, "candidate", spec.Phase)
			gotSlice = true
		}
	}
	assert.True(t, gotCell, "Cell entity present")
	assert.True(t, gotSlice, "Slice entity present")
}
