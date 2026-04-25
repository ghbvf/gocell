package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
)

func TestValidateSliceConsistency(t *testing.T) {
	tests := []struct {
		name           string
		cellLevel      string
		sliceLevel     string
		wantErrorCount int
		wantCode       string
	}{
		{
			name:           "slice with no explicit level inherits cell - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "",
			wantErrorCount: 0,
		},
		{
			name:           "slice level equals cell level - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "L2",
			wantErrorCount: 0,
		},
		{
			name:           "slice downgrade L1 in L2 cell - 0 findings",
			cellLevel:      "L2",
			sliceLevel:     "L1",
			wantErrorCount: 0,
		},
		{
			name:           "slice L0 in L3 cell - 0 findings",
			cellLevel:      "L3",
			sliceLevel:     "L0",
			wantErrorCount: 0,
		},
		{
			name:           "slice upgrades L3 in L2 cell - 1 error",
			cellLevel:      "L2",
			sliceLevel:     "L3",
			wantErrorCount: 1,
			wantCode:       "SLICE-CONSISTENCY-01",
		},
		{
			name:           "slice empty string treated as inherit - 0 findings",
			cellLevel:      "L1",
			sliceLevel:     "",
			wantErrorCount: 0,
		},
		{
			name:           "slice invalid level L9 - 1 error",
			cellLevel:      "L2",
			sliceLevel:     "L9",
			wantErrorCount: 1,
			wantCode:       "SLICE-CONSISTENCY-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					"testcell": {
						ID:               "testcell",
						ConsistencyLevel: tt.cellLevel,
					},
				},
				Slices: map[string]*metadata.SliceMeta{
					"testcell/testslice": {
						ID:               "testslice",
						BelongsToCell:    "testcell",
						ConsistencyLevel: tt.sliceLevel,
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}
			v := NewValidator(project, ".")
			results := v.validateSliceConsistency()

			var errCount int
			for _, r := range results {
				if r.Severity == SeverityError {
					errCount++
					if tt.wantCode != "" {
						assert.Equal(t, tt.wantCode, r.Code)
					}
				}
			}
			assert.Equal(t, tt.wantErrorCount, errCount, "unexpected error count")
		})
	}
}

// TestValidateSliceConsistency_MissingParentCell verifies that a slice with
// no registered parent cell is skipped (REF-01 covers the missing cell).
func TestValidateSliceConsistency_MissingParentCell(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{
			"ghostcell/testslice": {
				ID:               "testslice",
				BelongsToCell:    "ghostcell",
				ConsistencyLevel: "L3",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(project, ".")
	results := v.validateSliceConsistency()
	assert.Empty(t, results, "missing parent cell should be silently skipped")
}
