package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// TestLifecycleAggregator_Snapshot verifies the aggregator reports each registered
// cell's declared maturity lifecycle, sorted by cell ID for deterministic output.
// An empty lifecycle resolves to experimental (the NewBaseCell default).
func TestLifecycleAggregator_Snapshot(t *testing.T) {
	t.Parallel()
	configcore := cell.MustNewBaseCell(&metadata.CellMeta{ID: "configcore", Lifecycle: "asset"})
	auditcore := cell.MustNewBaseCell(&metadata.CellMeta{ID: "auditcore"}) // empty → experimental

	agg := NewLifecycleAggregator([]cell.CellIdentity{configcore, auditcore})
	snap := agg.Snapshot()

	require.Len(t, snap, 2)
	// Sorted by CellID: auditcore < configcore.
	assert.Equal(t, LifecycleEntry{CellID: "auditcore", Lifecycle: cellvocab.CellLifecycleExperimental}, snap[0])
	assert.Equal(t, LifecycleEntry{CellID: "configcore", Lifecycle: cellvocab.CellLifecycleAsset}, snap[1])
}

func TestLifecycleAggregator_Empty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, NewLifecycleAggregator(nil).Snapshot())
}

// TestLifecycleAggregator_Snapshot_DefensiveCopy asserts that mutating the slice
// returned by Snapshot does not affect subsequent Snapshot calls.
func TestLifecycleAggregator_Snapshot_DefensiveCopy(t *testing.T) {
	t.Parallel()
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"})
	agg := NewLifecycleAggregator([]cell.CellIdentity{c})

	snap1 := agg.Snapshot()
	require.Len(t, snap1, 1)
	// Mutate the returned slice — internal state must be unaffected.
	snap1[0] = LifecycleEntry{CellID: "mutated", Lifecycle: cellvocab.CellLifecycleRetired}

	snap2 := agg.Snapshot()
	require.Len(t, snap2, 1)
	assert.Equal(t, LifecycleEntry{CellID: "alpha", Lifecycle: cellvocab.CellLifecycleAsset}, snap2[0],
		"Snapshot must return a fresh copy, not the internal slice")
}

// TestLifecycleAggregator_SingleCell verifies a one-cell aggregator works correctly.
func TestLifecycleAggregator_SingleCell(t *testing.T) {
	t.Parallel()
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "only", Lifecycle: "candidate"})
	agg := NewLifecycleAggregator([]cell.CellIdentity{c})
	snap := agg.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, LifecycleEntry{CellID: "only", Lifecycle: cellvocab.CellLifecycleCandidate}, snap[0])
}

// TestLifecycleAggregator_SortedByCellID verifies that out-of-order input is
// returned sorted by cell ID.
func TestLifecycleAggregator_SortedByCellID(t *testing.T) {
	t.Parallel()
	z := cell.MustNewBaseCell(&metadata.CellMeta{ID: "zulu", Lifecycle: "retired"})
	m := cell.MustNewBaseCell(&metadata.CellMeta{ID: "mike"}) // empty → experimental
	a := cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"})

	// Input order: z, m, a — deliberately not sorted.
	agg := NewLifecycleAggregator([]cell.CellIdentity{z, m, a})
	snap := agg.Snapshot()

	require.Len(t, snap, 3)
	assert.Equal(t, "alpha", snap[0].CellID)
	assert.Equal(t, cellvocab.CellLifecycleAsset, snap[0].Lifecycle)
	assert.Equal(t, "mike", snap[1].CellID)
	assert.Equal(t, cellvocab.CellLifecycleExperimental, snap[1].Lifecycle)
	assert.Equal(t, "zulu", snap[2].CellID)
	assert.Equal(t, cellvocab.CellLifecycleRetired, snap[2].Lifecycle)
}

// TestLifecycleAggregator_Maintenance verifies the maintenance lifecycle is
// correctly tracked in the aggregator.
func TestLifecycleAggregator_Maintenance(t *testing.T) {
	t.Parallel()
	c := cell.MustNewBaseCell(&metadata.CellMeta{ID: "legacy", Lifecycle: "maintenance"})
	agg := NewLifecycleAggregator([]cell.CellIdentity{c})
	snap := agg.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, LifecycleEntry{CellID: "legacy", Lifecycle: cellvocab.CellLifecycleMaintenance}, snap[0])
}
