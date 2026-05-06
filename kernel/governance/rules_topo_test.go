package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- TOPO-09: assembly.MaxConsistencyLevel matches cells max ---

// buildTOPO09Project creates a minimal ProjectMeta with one assembly containing
// the given cells. Each cellID maps to the corresponding consistencyLevel entry.
func buildTOPO09Project(cellIDs []string, cellLevels []string, asmMaxLevel string) *metadata.ProjectMeta {
	cells := make(map[string]*metadata.CellMeta, len(cellIDs))
	for i, id := range cellIDs {
		cells[id] = &metadata.CellMeta{
			ID:               id,
			Type:             "core",
			ConsistencyLevel: cellLevels[i],
			Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
			Schema:           metadata.SchemaMeta{Primary: "cell_" + id},
			Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + id + ".startup"}},
			Dir:              id,
			File:             "cells/" + id + "/cell.yaml",
		}
	}
	return &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": {
				ID:                  "testasm",
				Cells:               cellIDs,
				MaxConsistencyLevel: asmMaxLevel,
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Dir:                 "testasm",
				File:                "assemblies/testasm/assembly.yaml",
			},
		},
	}
}

// TestTOPO09_AssemblyMaxConsistencyMatchesCells: derived MaxConsistencyLevel
// matches the actual cells max → 0 findings.
func TestTOPO09_AssemblyMaxConsistencyMatchesCells(t *testing.T) {
	// cells: L1 + L4 → expected max = L4
	pm := buildTOPO09Project(
		[]string{"cellA", "cellB"},
		[]string{"L1", "L4"},
		"L4", // correct derived value
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO09(), "TOPO-09")
	assert.Empty(t, got, "matching MaxConsistencyLevel should produce 0 findings")
}

// TestTOPO09_AssemblyMaxConsistencyDriftedFromCells: MaxConsistencyLevel is
// manually set to "L1" but cells contain an L4 cell → 1 finding, severity Error.
func TestTOPO09_AssemblyMaxConsistencyDriftedFromCells(t *testing.T) {
	pm := buildTOPO09Project(
		[]string{"cellA", "cellB"},
		[]string{"L1", "L4"},
		"L1", // wrong: cells max is L4
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO09(), "TOPO-09")
	require.Len(t, got, 1, "drifted MaxConsistencyLevel should produce 1 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueMismatch, got[0].IssueType)
	assert.Contains(t, got[0].Message, "testasm")
	assert.Contains(t, got[0].Message, "L1")
	assert.Contains(t, got[0].Message, "L4")
}

// TestTOPO09_AssemblyEmptyCells: assembly with no cells → skip, 0 findings.
func TestTOPO09_AssemblyEmptyCells(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"emptyasm": {
				ID:                  "emptyasm",
				Cells:               []string{},
				MaxConsistencyLevel: "",
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Dir:                 "emptyasm",
				File:                "assemblies/emptyasm/assembly.yaml",
			},
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO09(), "TOPO-09")
	assert.Empty(t, got, "assembly with no cells should be skipped")
}

// TestTOPO09_InvalidCellLevelSkips: assembly references a cell with an invalid
// consistencyLevel ("L9") → skip, 0 findings from TOPO-09.
// FMT-03 owns invalid level validation; TOPO-09 gracefully skips to avoid
// duplicate / misleading reports.
func TestTOPO09_InvalidCellLevelSkips(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"badlevelcell": {
				ID:               "badlevelcell",
				ConsistencyLevel: "L9", // invalid — FMT-03 covers this
				Type:             "core",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Dir:              "badlevelcell",
				File:             "cells/badlevelcell/cell.yaml",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"badasm": {
				ID:                  "badasm",
				Cells:               []string{"badlevelcell"},
				MaxConsistencyLevel: "L2",
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Dir:                 "badasm",
				File:                "assemblies/badasm/assembly.yaml",
			},
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO09(), "TOPO-09")
	assert.Empty(t, got, "invalid cell level should be skipped by TOPO-09 (FMT-03 owns invalid level validation)")
}

// TestTOPO09_AssemblyUnknownCellRef: assembly references a cell ID not in
// Cells map → skip (REF rules cover unknown refs), 0 findings from TOPO-09.
func TestTOPO09_AssemblyUnknownCellRef(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{}, // no cells registered
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"ghostasm": {
				ID:                  "ghostasm",
				Cells:               []string{"unknown-cell"},
				MaxConsistencyLevel: "L2",
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Dir:                 "ghostasm",
				File:                "assemblies/ghostasm/assembly.yaml",
			},
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO09(), "TOPO-09")
	assert.Empty(t, got, "unknown cell ref should be skipped by TOPO-09 (covered by REF rules)")
}
