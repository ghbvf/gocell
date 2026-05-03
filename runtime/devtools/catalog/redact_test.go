// Package catalog_test — redact_test.go: redact integration tests via BuildDocument.
package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// TestRedact_SpeculativeStatesCleared verifies that risk/blocker are cleared for
// draft and planned entries.
func TestRedact_SpeculativeStatesCleared(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-1", State: "draft", Risk: "high", Blocker: "secret plan", UpdatedAt: "2026-05-01"},
			{JourneyID: "J-2", State: "planned", Risk: "medium", Blocker: "private note", UpdatedAt: "2026-05-02"},
		},
	}
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Include: catalog.IncludeOptions{StatusBoard: true}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.Len(t, doc.StatusBoard, 2)

	for _, e := range doc.StatusBoard {
		assert.Empty(t, e.Risk, "risk must be redacted for state=%s", e.State)
		assert.Empty(t, e.Blocker, "blocker must be redacted for state=%s", e.State)
		assert.NotEmpty(t, e.JourneyID, "journeyId must be preserved")
		assert.NotEmpty(t, e.State, "state must be preserved")
		assert.NotEmpty(t, e.UpdatedAt, "updatedAt must be preserved")
	}
}

// TestRedact_OperationalStatesPreserved verifies that risk/blocker are preserved
// for doing, blocked, and ready entries.
func TestRedact_OperationalStatesPreserved(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-3", State: "doing", Risk: "low", Blocker: "minor issue", UpdatedAt: "2026-05-03"},
			{JourneyID: "J-4", State: "blocked", Risk: "high", Blocker: "critical dep", UpdatedAt: "2026-05-04"},
			{JourneyID: "J-5", State: "ready", Risk: "none", Blocker: "", UpdatedAt: "2026-05-05"},
		},
	}
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Include: catalog.IncludeOptions{StatusBoard: true}},
	}
	doc, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)
	require.Len(t, doc.StatusBoard, 3)

	byID := make(map[string]catalog.StatusBoardEntry, 3)
	for _, e := range doc.StatusBoard {
		byID[e.JourneyID] = e
	}

	doing := byID["J-3"]
	assert.Equal(t, "low", doing.Risk)
	assert.Equal(t, "minor issue", doing.Blocker)

	blocked := byID["J-4"]
	assert.Equal(t, "high", blocked.Risk)
	assert.Equal(t, "critical dep", blocked.Blocker)

	ready := byID["J-5"]
	assert.Equal(t, "none", ready.Risk)
	assert.Empty(t, ready.Blocker)
}

// TestRedact_DoesNotMutatePM verifies that redact does not mutate the original
// ProjectMeta status board entries.
func TestRedact_DoesNotMutatePM(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-draft", State: "draft", Risk: "high", Blocker: "secret", UpdatedAt: "2026-05-01"},
		},
	}
	origRisk := pm.StatusBoard[0].Risk
	origBlocker := pm.StatusBoard[0].Blocker

	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Filter: catalog.Filter{Include: catalog.IncludeOptions{StatusBoard: true}},
	}
	_, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	assert.Equal(t, origRisk, pm.StatusBoard[0].Risk, "original Risk must not be mutated")
	assert.Equal(t, origBlocker, pm.StatusBoard[0].Blocker, "original Blocker must not be mutated")
}
