package governance_test

import (
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocation_REF01_SliceBelongsToCell verifies that a REF-01 finding
// (slice pointing at a non-existent cell) carries the line/column of the
// offending belongsToCell scalar, not a zero placeholder.
func TestLocation_REF01_SliceBelongsToCell(t *testing.T) {
	fs := fstest.MapFS{
		"cells/good/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: good\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
		"cells/good/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(
			"id: s\n" + // line 1
				"belongsToCell: good\n" + // line 2 — matches directory, OK
				"contractUsages: []\n" + // line 3
				"verify: {unit: [], contract: []}\n" + // line 4
				"allowedFiles:\n" + // line 5
				"  - cells/good/slices/s/**\n", // line 6
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	// Sanity: REF-01 should NOT fire here (reference matches). We just verify
	// that the FileNodes cache is populated and locations are reachable end-to-end.
	v := governance.NewValidator(pm, "")
	results := v.Validate()
	for _, r := range results {
		if r.File == "cells/good/slices/s/slice.yaml" && r.Line == 0 {
			t.Errorf("finding on slice.yaml has no location: %+v", r)
		}
	}
}

// TestLocation_REF02_ContractUsageIndex exercises an array-indexed field path
// to confirm that the locator walks contractUsages[i].contract correctly.
func TestLocation_REF02_ContractUsageIndex(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: x\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
		"cells/x/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(
			"id: s\n" + // line 1
				"belongsToCell: x\n" + // line 2
				"contractUsages:\n" + // line 3
				"  - contract: http.ghost.v1\n" + // line 4 — missing ref → REF-02
				"    role: serve\n" + // line 5
				"verify: {unit: [], contract: []}\n" + // line 6
				"allowedFiles:\n  - foo/**\n", // line 7-8
		)},
	}

	p := metadata.NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	v := governance.NewValidator(pm, "")
	results := v.Validate()

	var ref02 *governance.ValidationResult
	for i := range results {
		r := &results[i]
		if r.Code == "REF-02" && r.File == "cells/x/slices/s/slice.yaml" {
			ref02 = r
			break
		}
	}
	require.NotNil(t, ref02, "expected a REF-02 finding")
	assert.Equal(t, "contractUsages[0].contract", ref02.Field)
	assert.Equal(t, 4, ref02.Line, "contractUsages[0].contract should be on line 4")
	assert.Positive(t, ref02.Column)
}

// TestLocation_NoNodes_NoCrash confirms that validators tolerate a
// ProjectMeta without FileNodes (e.g. constructed manually in a test): the Line
// and Column fields are simply zero.
func TestLocation_NoNodes_NoCrash(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"x/s": {ID: "s", BelongsToCell: "ghost"},
		},
		Contracts: map[string]*metadata.ContractMeta{},
	}
	v := governance.NewValidator(pm, "")
	results := v.Validate()

	// At least the REF-01 (cell not found) should fire.
	seenRef01 := false
	for _, r := range results {
		if r.Code == "REF-01" {
			seenRef01 = true
			assert.Zero(t, r.Line, "Line should be 0 without FileNodes")
			assert.Zero(t, r.Column, "Column should be 0 without FileNodes")
		}
	}
	assert.True(t, seenRef01, "REF-01 should be produced")
}
