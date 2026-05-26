package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestPhaseAggregator_Snapshot verifies the aggregator reports each registered
// cell's declared maturity phase, sorted by cell ID for deterministic output.
// An empty lifecycle resolves to experimental (the NewBaseCell default).
func TestPhaseAggregator_Snapshot(t *testing.T) {
	t.Parallel()
	configcore := cell.MustNewBaseCell(&metadata.CellMeta{ID: "configcore", Lifecycle: "asset"})
	auditcore := cell.MustNewBaseCell(&metadata.CellMeta{ID: "auditcore"}) // empty → experimental

	agg := NewPhaseAggregator([]cell.CellIdentity{configcore, auditcore})
	snap := agg.Snapshot()

	require.Len(t, snap, 2)
	// Sorted by CellID: auditcore < configcore.
	assert.Equal(t, PhaseEntry{CellID: "auditcore", Phase: cellvocab.PhaseExperimental}, snap[0])
	assert.Equal(t, PhaseEntry{CellID: "configcore", Phase: cellvocab.PhaseAsset}, snap[1])
}

func TestPhaseAggregator_Empty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, NewPhaseAggregator(nil).Snapshot())
}
