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

// TestPhaseAggregator_Snapshot_DefensiveCopy asserts that mutating the slice
// returned by Snapshot does not affect subsequent Snapshot calls.
func TestPhaseAggregator_Snapshot_DefensiveCopy(t *testing.T) {
	t.Parallel()
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"})
	agg := NewPhaseAggregator([]cell.CellIdentity{c})

	snap1 := agg.Snapshot()
	require.Len(t, snap1, 1)
	// Mutate the returned slice — internal state must be unaffected.
	snap1[0] = PhaseEntry{CellID: "mutated", Phase: cellvocab.PhaseRetired}

	snap2 := agg.Snapshot()
	require.Len(t, snap2, 1)
	assert.Equal(t, PhaseEntry{CellID: "alpha", Phase: cellvocab.PhaseAsset}, snap2[0],
		"Snapshot must return a fresh copy, not the internal slice")
}

// TestPhaseAggregator_SingleCell verifies a one-cell aggregator works correctly.
func TestPhaseAggregator_SingleCell(t *testing.T) {
	t.Parallel()
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "only", Lifecycle: "candidate"})
	agg := NewPhaseAggregator([]cell.CellIdentity{c})
	snap := agg.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, PhaseEntry{CellID: "only", Phase: cellvocab.PhaseCandidate}, snap[0])
}

// TestPhaseAggregator_SortedByCellID verifies that out-of-order input is
// returned sorted by cell ID.
func TestPhaseAggregator_SortedByCellID(t *testing.T) {
	t.Parallel()
	z := cell.MustNewBaseCell(&metadata.CellMeta{ID: "zulu", Lifecycle: "retired"})
	m := cell.MustNewBaseCell(&metadata.CellMeta{ID: "mike"}) // empty → experimental
	a := cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"})

	// Input order: z, m, a — deliberately not sorted.
	agg := NewPhaseAggregator([]cell.CellIdentity{z, m, a})
	snap := agg.Snapshot()

	require.Len(t, snap, 3)
	assert.Equal(t, "alpha", snap[0].CellID)
	assert.Equal(t, cellvocab.PhaseAsset, snap[0].Phase)
	assert.Equal(t, "mike", snap[1].CellID)
	assert.Equal(t, cellvocab.PhaseExperimental, snap[1].Phase)
	assert.Equal(t, "zulu", snap[2].CellID)
	assert.Equal(t, cellvocab.PhaseRetired, snap[2].Phase)
}
